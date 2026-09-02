package projectstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

func CanonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeCanonicalValue(&buffer, reflect.ValueOf(value)); err != nil {
		return nil, fmt.Errorf("%w: canonical JSON: %v", ErrInvalidSnapshot, err)
	}
	buffer.WriteByte('\n')
	return buffer.Bytes(), nil
}

// CanonicalJSONObject returns the compact, recursively key-sorted object form
// used on strict public wire boundaries. CanonicalJSON retains declaration
// order for typed project-state files; public request objects require the
// encoding/json object order independently of their Go struct declarations.
func CanonicalJSONObject(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical JSON object: %v", ErrInvalidSnapshot, err)
	}
	var object any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, fmt.Errorf("%w: canonical JSON object: %v", ErrInvalidSnapshot, err)
	}
	if _, ok := object.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: canonical JSON object: top-level value is not an object", ErrInvalidSnapshot)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical JSON object: %v", ErrInvalidSnapshot, err)
	}
	return canonical, nil
}

func CanonicalMarkdown(body []byte) ([]byte, error) {
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 {
		return nil, fmt.Errorf("%w: Markdown must be NUL-free UTF-8", ErrInvalidSnapshot)
	}
	canonical := strings.ReplaceAll(string(body), "\r\n", "\n")
	canonical = strings.ReplaceAll(canonical, "\r", "\n")
	canonical = strings.TrimRight(canonical, "\n") + "\n"
	return []byte(canonical), nil
}

func DigestCanonicalJSON(value any) (Digest, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return digestCanonicalBytes(canonical), nil
}

func DigestCanonicalMarkdown(body []byte) (Digest, error) {
	canonical, err := CanonicalMarkdown(body)
	if err != nil {
		return "", err
	}
	return digestCanonicalBytes(canonical), nil
}

func digestCanonicalBytes(value []byte) Digest {
	digest := sha256.Sum256(value)
	return Digest("sha256:" + hex.EncodeToString(digest[:]))
}

func DigestTree(tree Tree) (Digest, error) {
	files := cloneAndSortTree(tree)
	if err := validateTreePaths(files); err != nil {
		return "", err
	}
	hash := sha256.New()
	var length [8]byte
	for _, file := range files {
		binary.BigEndian.PutUint64(length[:], uint64(len(file.Path)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(file.Path))
		binary.BigEndian.PutUint64(length[:], uint64(len(file.Data)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write(file.Data)
	}
	return Digest("sha256:" + hex.EncodeToString(hash.Sum(nil))), nil
}

func DecodeTree(tree Tree) (Snapshot, error) {
	files := cloneAndSortTree(tree)
	if err := validateTreePaths(files); err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Actors: make(map[string]Record[ActorV1]), Tasks: make(map[string]Record[TaskV1]),
		TaskLinks: make(map[string]Record[TaskLinkV1]), Articles: make(map[string]KBRecord),
		Channels: make(map[string]Record[ChannelV1]), Events: make(map[string]EventV1),
		GitLinks: make(map[string]Record[GitLinkV1]),
	}
	seenConfig := false
	seenProject := false
	for _, file := range files {
		var err error
		switch {
		case file.Path == "config.toml":
			if seenConfig {
				return Snapshot{}, invalidPath(file.Path, "duplicate config")
			}
			snapshot.Config, err = decodeConfig(file.Data)
			seenConfig = true
		case file.Path == "remotes.toml":
			if snapshot.Remotes != nil {
				return Snapshot{}, invalidPath(file.Path, "duplicate remotes")
			}
			var remotes RemotesV1
			remotes, err = decodeRemotes(file.Data)
			snapshot.Remotes = &remotes
		case file.Path == "state/v1/project.json":
			if seenProject {
				return Snapshot{}, invalidPath(file.Path, "duplicate project")
			}
			err = decodeTypedJSON(file.Path, file.Data, "project", &snapshot.Project)
			seenProject = true
		case matchPath(file.Path, "state/v1/actors/", ".json") != "":
			id := matchPath(file.Path, "state/v1/actors/", ".json")
			var record Record[ActorV1]
			record, err = decodeRecord[ActorV1](file.Path, file.Data, "actor", id, true)
			if err == nil {
				snapshot.Actors[id] = record
			}
		case matchPath(file.Path, "state/v1/tasks/links/", ".json") != "":
			id := matchPath(file.Path, "state/v1/tasks/links/", ".json")
			var record Record[TaskLinkV1]
			record, err = decodeRecord[TaskLinkV1](file.Path, file.Data, "task_link", id, true)
			if err == nil {
				snapshot.TaskLinks[id] = record
			}
		case matchPath(file.Path, "state/v1/tasks/", ".json") != "":
			id := matchPath(file.Path, "state/v1/tasks/", ".json")
			var record Record[TaskV1]
			record, err = decodeRecord[TaskV1](file.Path, file.Data, "task", id, true)
			if err == nil {
				snapshot.Tasks[id] = record
			}
		case kbPathID(file.Path, "/record.json") != "":
			id := kbPathID(file.Path, "/record.json")
			var record Record[KBArticleV1]
			record, err = decodeRecord[KBArticleV1](file.Path, file.Data, "kb_article", id, true)
			if err == nil {
				article := snapshot.Articles[id]
				article.Value, article.Tombstone = record.Value, record.Tombstone
				snapshot.Articles[id] = article
			}
		case kbPathID(file.Path, "/body.md") != "":
			id := kbPathID(file.Path, "/body.md")
			canonical, markdownErr := CanonicalMarkdown(file.Data)
			if markdownErr != nil || !bytes.Equal(canonical, file.Data) {
				if markdownErr != nil {
					err = markdownErr
				} else {
					err = invalidPath(file.Path, "non-canonical Markdown")
				}
			} else {
				article := snapshot.Articles[id]
				article.Body = bytes.Clone(file.Data)
				snapshot.Articles[id] = article
			}
		case matchPath(file.Path, "state/v1/channels/", ".json") != "":
			id := matchPath(file.Path, "state/v1/channels/", ".json")
			var record Record[ChannelV1]
			record, err = decodeRecord[ChannelV1](file.Path, file.Data, "channel", id, true)
			if err == nil {
				snapshot.Channels[id] = record
			}
		case matchPath(file.Path, "state/v1/events/", ".json") != "":
			id := matchPath(file.Path, "state/v1/events/", ".json")
			var event EventV1
			err = decodeTypedJSON(file.Path, file.Data, "event", &event)
			if err == nil && event.ID != id {
				err = invalidPath(file.Path, "path and record ID differ")
			}
			if err == nil {
				snapshot.Events[id] = event
			}
		case matchPath(file.Path, "state/v1/git-links/", ".json") != "":
			id := matchPath(file.Path, "state/v1/git-links/", ".json")
			var record Record[GitLinkV1]
			record, err = decodeRecord[GitLinkV1](file.Path, file.Data, "git_link", id, false)
			if err == nil {
				snapshot.GitLinks[id] = record
			}
		default:
			err = invalidPath(file.Path, "unknown tree path")
		}
		if err != nil {
			return Snapshot{}, err
		}
	}
	if !seenConfig || !seenProject {
		return Snapshot{}, fmt.Errorf("%w: config.toml and project.json are required", ErrInvalidSnapshot)
	}
	if err := Validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	rendered, err := encodeTreeUnchecked(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	if !treesEqualByPath(files, rendered) {
		return Snapshot{}, fmt.Errorf("%w: tree is not canonically encoded", ErrInvalidSnapshot)
	}
	snapshot.Digest, err = DigestTree(rendered)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func EncodeTree(snapshot Snapshot) (Tree, error) {
	if err := Validate(snapshot); err != nil {
		return nil, err
	}
	return encodeTreeUnchecked(snapshot)
}

func encodeTreeUnchecked(snapshot Snapshot) (Tree, error) {
	config, err := encodeConfig(snapshot.Config)
	if err != nil {
		return nil, err
	}
	tree := Tree{{Path: "config.toml", Data: config}}
	if snapshot.Remotes != nil {
		remotes, remotesErr := encodeRemotes(*snapshot.Remotes)
		if remotesErr != nil {
			return nil, remotesErr
		}
		tree = append(tree, File{Path: "remotes.toml", Data: remotes})
	}
	if err := appendJSONFile(&tree, "state/v1/project.json", snapshot.Project); err != nil {
		return nil, err
	}
	for _, id := range sortedRecordKeys(snapshot.Actors) {
		if err := appendRecordFile(&tree, "state/v1/actors/"+id+".json", snapshot.Actors[id]); err != nil {
			return nil, err
		}
	}
	for _, id := range sortedRecordKeys(snapshot.Tasks) {
		if err := appendRecordFile(&tree, "state/v1/tasks/"+id+".json", snapshot.Tasks[id]); err != nil {
			return nil, err
		}
	}
	for _, id := range sortedRecordKeys(snapshot.TaskLinks) {
		if err := appendRecordFile(&tree, "state/v1/tasks/links/"+id+".json", snapshot.TaskLinks[id]); err != nil {
			return nil, err
		}
	}
	articleIDs := make([]string, 0, len(snapshot.Articles))
	for id := range snapshot.Articles {
		articleIDs = append(articleIDs, id)
	}
	sort.Strings(articleIDs)
	for _, id := range articleIDs {
		record := snapshot.Articles[id]
		if record.Value != nil {
			if err := appendJSONFile(&tree, "state/v1/kb/"+id+"/record.json", *record.Value); err != nil {
				return nil, err
			}
			body, markdownErr := CanonicalMarkdown(record.Body)
			if markdownErr != nil {
				return nil, markdownErr
			}
			tree = append(tree, File{Path: "state/v1/kb/" + id + "/body.md", Data: body})
		} else if err := appendJSONFile(&tree, "state/v1/kb/"+id+"/record.json", *record.Tombstone); err != nil {
			return nil, err
		}
	}
	for _, id := range sortedRecordKeys(snapshot.Channels) {
		if err := appendRecordFile(&tree, "state/v1/channels/"+id+".json", snapshot.Channels[id]); err != nil {
			return nil, err
		}
	}
	eventIDs := make([]string, 0, len(snapshot.Events))
	for id := range snapshot.Events {
		eventIDs = append(eventIDs, id)
	}
	sort.Strings(eventIDs)
	for _, id := range eventIDs {
		if err := appendJSONFile(&tree, "state/v1/events/"+id+".json", snapshot.Events[id]); err != nil {
			return nil, err
		}
	}
	for _, id := range sortedRecordKeys(snapshot.GitLinks) {
		if err := appendRecordFile(&tree, "state/v1/git-links/"+id+".json", snapshot.GitLinks[id]); err != nil {
			return nil, err
		}
	}
	sort.Slice(tree, func(i, j int) bool { return tree[i].Path < tree[j].Path })
	return tree, nil
}

func appendRecordFile[T any](tree *Tree, path string, record Record[T]) error {
	if record.Value != nil {
		return appendJSONFile(tree, path, *record.Value)
	}
	return appendJSONFile(tree, path, *record.Tombstone)
}

func appendJSONFile(tree *Tree, path string, value any) error {
	data, err := CanonicalJSON(value)
	if err != nil {
		return err
	}
	*tree = append(*tree, File{Path: path, Data: data})
	return nil
}

func sortedRecordKeys[T any](records map[string]Record[T]) []string {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func decodeRecord[T any](filePath string, data []byte, entityKind, pathID string, tombstoneAllowed bool) (Record[T], error) {
	header, err := decodeHeader(filePath, data)
	if err != nil {
		return Record[T]{}, err
	}
	if header.ID != pathID {
		return Record[T]{}, invalidPath(filePath, "path and record ID differ")
	}
	if header.Kind == "tombstone" {
		if !tombstoneAllowed {
			return Record[T]{}, fmt.Errorf("%w: tombstone not allowed at %s", ErrUnknownKind, filePath)
		}
		var tombstone TombstoneV1
		if err := decodeJSON(filePath, data, &tombstone); err != nil {
			return Record[T]{}, err
		}
		if tombstone.EntityKind != entityKind {
			return Record[T]{}, invalidPath(filePath, "tombstone entity kind differs from path")
		}
		return Record[T]{Tombstone: &tombstone}, nil
	}
	if header.Kind != entityKind {
		return Record[T]{}, fmt.Errorf("%w: %s has kind %q", ErrUnknownKind, filePath, header.Kind)
	}
	var value T
	if err := decodeJSON(filePath, data, &value); err != nil {
		return Record[T]{}, err
	}
	return Record[T]{Value: &value}, nil
}

func decodeTypedJSON(filePath string, data []byte, expectedKind string, destination any) error {
	header, err := decodeHeader(filePath, data)
	if err != nil {
		return err
	}
	if header.Kind != expectedKind {
		return fmt.Errorf("%w: %s has kind %q", ErrUnknownKind, filePath, header.Kind)
	}
	return decodeJSON(filePath, data, destination)
}

type recordHeader struct {
	SchemaVersion int
	Kind          string
	ID            string
}

func decodeHeader(filePath string, data []byte) (recordHeader, error) {
	if err := rejectTrackedJSONSecrets(data); err != nil {
		return recordHeader{}, fmt.Errorf("%w: %s", err, filePath)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return recordHeader{}, invalidPath(filePath, "malformed JSON")
	}
	var header recordHeader
	if err := json.Unmarshal(fields["schema_version"], &header.SchemaVersion); err != nil {
		return recordHeader{}, invalidPath(filePath, "missing schema_version")
	}
	if header.SchemaVersion != 1 {
		return recordHeader{}, fmt.Errorf("%w: %s schema_version=%d", ErrUnknownVersion, filePath, header.SchemaVersion)
	}
	if err := json.Unmarshal(fields["kind"], &header.Kind); err != nil {
		return recordHeader{}, invalidPath(filePath, "missing kind")
	}
	if err := json.Unmarshal(fields["id"], &header.ID); err != nil {
		return recordHeader{}, invalidPath(filePath, "missing id")
	}
	return header, nil
}

func decodeJSON(filePath string, data []byte, destination any) error {
	if err := rejectTrackedJSONSecrets(data); err != nil {
		return fmt.Errorf("%w: %s", err, filePath)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return invalidPath(filePath, "strict JSON decode: "+err.Error())
	}
	if err := requireJSONEOF(decoder); err != nil {
		return invalidPath(filePath, err.Error())
	}
	canonical, err := CanonicalJSON(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return invalidPath(filePath, "non-canonical JSON")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errorsText("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeConfig(data []byte) (ConfigV1, error) {
	if err := rejectTrackedTOMLSecrets(data); err != nil {
		return ConfigV1{}, err
	}
	var config ConfigV1
	metadata, err := toml.Decode(string(data), &config)
	if err != nil {
		return ConfigV1{}, invalidPath("config.toml", "TOML decode: "+err.Error())
	}
	if len(metadata.Undecoded()) != 0 {
		return ConfigV1{}, invalidPath("config.toml", "unknown TOML key")
	}
	if config.SnapshotVersion != 1 {
		return ConfigV1{}, fmt.Errorf("%w: config snapshot_version=%d", ErrUnknownVersion, config.SnapshotVersion)
	}
	canonical, err := encodeConfig(config)
	if err != nil {
		return ConfigV1{}, err
	}
	if !bytes.Equal(canonical, data) {
		return ConfigV1{}, invalidPath("config.toml", "non-canonical TOML")
	}
	return config, nil
}

func decodeRemotes(data []byte) (RemotesV1, error) {
	if err := rejectTrackedTOMLSecrets(data); err != nil {
		return RemotesV1{}, err
	}
	var remotes RemotesV1
	metadata, err := toml.Decode(string(data), &remotes)
	if err != nil {
		return RemotesV1{}, invalidPath("remotes.toml", "TOML decode: "+err.Error())
	}
	if len(metadata.Undecoded()) != 0 {
		return RemotesV1{}, invalidPath("remotes.toml", "unknown TOML key")
	}
	if remotes.Version != 1 {
		return RemotesV1{}, fmt.Errorf("%w: remotes version=%d", ErrUnknownVersion, remotes.Version)
	}
	canonical, err := encodeRemotes(remotes)
	if err != nil {
		return RemotesV1{}, err
	}
	if !bytes.Equal(canonical, data) {
		return RemotesV1{}, invalidPath("remotes.toml", "non-canonical TOML")
	}
	return remotes, nil
}

func encodeConfig(config ConfigV1) ([]byte, error) {
	var builder strings.Builder
	fmt.Fprintf(&builder, "snapshot_version = %d\nproject_id = %s\n\n", config.SnapshotVersion, strconv.Quote(config.ProjectID))
	fmt.Fprintf(&builder, "[handle]\nnamespace = %s\nname = %s\n\n", strconv.Quote(config.Handle.Namespace), strconv.Quote(config.Handle.Name))
	fmt.Fprintf(&builder, "[repository]\nprovider = %s\nimmutable_id = %s\ncanonical_remote = %s\n", strconv.Quote(config.Repository.Provider), strconv.Quote(config.Repository.ImmutableID), strconv.Quote(config.Repository.CanonicalRemote))
	return []byte(builder.String()), nil
}

func encodeRemotes(remotes RemotesV1) ([]byte, error) {
	fabrics := append([]FabricHintV1(nil), remotes.Fabrics...)
	sort.Slice(fabrics, func(i, j int) bool { return fabrics[i].Alias < fabrics[j].Alias })
	var builder strings.Builder
	fmt.Fprintf(&builder, "version = %d\n", remotes.Version)
	for _, fabric := range fabrics {
		fmt.Fprintf(&builder, "\n[[fabric]]\nalias = %s\nurl = %s\ninstance_id = %s\nremote_project_id = %s\nmode = %s\n\n", strconv.Quote(fabric.Alias), strconv.Quote(fabric.URL), strconv.Quote(fabric.InstanceID), strconv.Quote(fabric.RemoteProjectID), strconv.Quote(fabric.Mode))
		fmt.Fprintf(&builder, "[fabric.expected_repository]\nprovider = %s\nimmutable_id = %s\ncanonical_remote = %s\n", strconv.Quote(fabric.ExpectedRepository.Provider), strconv.Quote(fabric.ExpectedRepository.ImmutableID), strconv.Quote(fabric.ExpectedRepository.CanonicalRemote))
	}
	return []byte(builder.String()), nil
}

func rejectTrackedJSONSecrets(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalidPath("JSON", "malformed JSON")
	}
	if hasSecretKey(value) {
		return ErrTrackedSecret
	}
	return nil
}

func rejectTrackedTOMLSecrets(data []byte) error {
	var value map[string]any
	if _, err := toml.Decode(string(data), &value); err != nil {
		return invalidPath("TOML", "malformed TOML")
	}
	if hasSecretKey(value) {
		return ErrTrackedSecret
	}
	return nil
}

func hasSecretKey(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if secretKey(key) || hasSecretKey(child) {
				return true
			}
		}
	case []map[string]any:
		for _, child := range typed {
			if hasSecretKey(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasSecretKey(child) {
				return true
			}
		}
	}
	return false
}

func secretKey(key string) bool {
	switch strings.ToLower(key) {
	case "token", "access_token", "refresh_token", "password", "secret", "private_key", "passport", "credential", "session_cookie", "absolute_path", "checkout_path", "workspace_id":
		return true
	default:
		return false
	}
}

func writeCanonicalValue(buffer *bytes.Buffer, value reflect.Value) error {
	if !value.IsValid() || ((value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil()) {
		buffer.WriteString("null")
		return nil
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		return writeCanonicalValue(buffer, value.Elem())
	}
	if value.Type() == reflect.TypeOf(json.RawMessage{}) {
		return writeCanonicalRaw(buffer, value.Interface().(json.RawMessage))
	}
	if value.Type() == reflect.TypeOf(json.Number("")) {
		number := value.Interface().(json.Number)
		if _, err := strconv.ParseFloat(number.String(), 64); err != nil {
			return err
		}
		buffer.WriteString(number.String())
		return nil
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		encoded, err := json.Marshal(value.Interface())
		if err != nil {
			return err
		}
		buffer.Write(encoded)
		return nil
	}
	switch value.Kind() {
	case reflect.Struct:
		buffer.WriteByte('{')
		written := 0
		for i := 0; i < value.NumField(); i++ {
			fieldType := value.Type().Field(i)
			if fieldType.PkgPath != "" {
				continue
			}
			tag := fieldType.Tag.Get("json")
			if tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "" {
				name = fieldType.Name
			}
			omitEmpty := len(parts) > 1 && parts[1] == "omitempty"
			field := value.Field(i)
			if omitEmpty && field.IsZero() {
				continue
			}
			if written > 0 {
				buffer.WriteByte(',')
			}
			encodedName, _ := json.Marshal(name)
			buffer.Write(encodedName)
			buffer.WriteByte(':')
			if err := writeCanonicalValue(buffer, field); err != nil {
				return err
			}
			written++
		}
		buffer.WriteByte('}')
	case reflect.Map:
		if value.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("map key type %s is not string", value.Type().Key())
		}
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		buffer.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buffer.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key.String())
			buffer.Write(encodedKey)
			buffer.WriteByte(':')
			if err := writeCanonicalValue(buffer, value.MapIndex(key)); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			encoded, err := json.Marshal(value.Interface())
			if err != nil {
				return err
			}
			buffer.Write(encoded)
			return nil
		}
		buffer.WriteByte('[')
		for i := 0; i < value.Len(); i++ {
			if i > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalValue(buffer, value.Index(i)); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	default:
		encoded, err := json.Marshal(value.Interface())
		if err != nil {
			return err
		}
		buffer.Write(encoded)
	}
	return nil
}

func writeCanonicalRaw(buffer *bytes.Buffer, data json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	return writeCanonicalValue(buffer, reflect.ValueOf(value))
}

func cloneAndSortTree(tree Tree) Tree {
	files := make(Tree, len(tree))
	for i, file := range tree {
		files[i] = File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func validateTreePaths(files Tree) error {
	for i, file := range files {
		if file.Path == "" || strings.ContainsRune(file.Path, 0) || strings.Contains(file.Path, "\\") || strings.HasPrefix(file.Path, "/") || path.Clean(file.Path) != file.Path || file.Path == "." || file.Path == ".." || strings.HasPrefix(file.Path, "../") {
			return invalidPath(file.Path, "unsafe path")
		}
		if i > 0 && files[i-1].Path == file.Path {
			return invalidPath(file.Path, "duplicate path")
		}
	}
	return nil
}

func matchPath(value, prefix, suffix string) string {
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(value, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func kbPathID(value, suffix string) string {
	return matchPath(value, "state/v1/kb/", suffix)
}

func treesEqualByPath(left, right Tree) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Path != right[i].Path || !bytes.Equal(left[i].Data, right[i].Data) {
			return false
		}
	}
	return true
}

func invalidPath(filePath, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidSnapshot, filePath, reason)
}

func errorsText(value string) error { return fmt.Errorf("%s", value) }

func validateFabricURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
