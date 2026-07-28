package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	cohereHost       = "api.cohere.com"
	embeddingSize    = 1024
	maxRequestBody   = 2 * 1024 * 1024
	requestTimeout   = 2 * time.Second
	certificateValid = time.Hour
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "probe-health" {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()
		if err := probeHealth(ctx, os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) != 1 {
		log.Fatal("usage: mock [probe-health URL]")
	}
	apiKey := os.Getenv("WORMHOLE_MOCK_API_KEY")
	caPath := os.Getenv("WORMHOLE_MOCK_CA_PATH")
	countPath := os.Getenv("WORMHOLE_MOCK_COUNT_PATH")
	if apiKey == "" || caPath == "" || countPath == "" {
		log.Fatal("mock API key, CA path, and count path are required")
	}
	if err := serveCohereMock(apiKey, caPath, countPath); err != nil {
		log.Fatal(err)
	}
}

func serveCohereMock(apiKey, caPath, countPath string) error {
	certificate, caPEM, err := generateTLSMaterial(time.Now())
	if err != nil {
		return fmt.Errorf("generate TLS material: %w", err)
	}
	listener, err := net.Listen("tcp", ":443")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	if err := writeReadyCA(caPath, caPEM); err != nil {
		return fmt.Errorf("publish CA: %w", err)
	}
	server := &http.Server{
		Handler:           newCohereMock(apiKey, countPath).handler(),
		ReadTimeout:       requestTimeout,
		WriteTimeout:      requestTimeout,
		ReadHeaderTimeout: requestTimeout,
		IdleTimeout:       requestTimeout,
		MaxHeaderBytes:    16 * 1024,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS13,
		},
	}
	return server.ServeTLS(listener, "", "")
}

type cohereMock struct {
	apiKey       string
	countPath    string
	mu           sync.Mutex
	requestCount int
}

func newCohereMock(apiKey, countPath string) *cohereMock {
	return &cohereMock{apiKey: apiKey, countPath: countPath}
}

func (m *cohereMock) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount, err := m.recordRequest()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if requestCount != 1 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if r.URL.Path != "/v1/embed" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+m.apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		var request struct {
			Model           string   `json:"model"`
			Texts           []string `json:"texts"`
			InputType       string   `json:"input_type"`
			EmbeddingTypes  []string `json:"embedding_types"`
			OutputDimension int      `json:"output_dimension"`
			MaxTokens       int      `json:"max_tokens"`
			Truncate        string   `json:"truncate"`
		}
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody+1))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Model != "embed-v4.0" ||
			request.InputType != "search_document" || request.OutputDimension != embeddingSize ||
			len(request.Texts) != 1 || request.Texts[0] == "" ||
			len(request.EmbeddingTypes) != 1 || request.EmbeddingTypes[0] != "float" ||
			request.MaxTokens != 8192 || request.Truncate != "NONE" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		vector := make([]float32, embeddingSize)
		vector[0] = 1
		response := struct {
			Texts      []string `json:"texts"`
			Embeddings struct {
				Float [][]float32 `json:"float"`
			} `json:"embeddings"`
		}{Texts: request.Texts}
		response.Embeddings.Float = [][]float32{vector}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
}

func (m *cohereMock) recordRequest() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requestCount++
	temporary := m.countPath + ".tmp"
	data := []byte(strconv.Itoa(m.requestCount) + "\n")
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return m.requestCount, err
	}
	if err := os.Rename(temporary, m.countPath); err != nil {
		return m.requestCount, err
	}
	return m.requestCount, nil
}

func probeHealth(ctx context.Context, url string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	response, err := (&http.Client{Timeout: requestTimeout}).Do(request)
	if err != nil {
		return fmt.Errorf("request health endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("health status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}
	return nil
}

func generateTLSMaterial(now time.Time) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caSerial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caTemplate := x509.Certificate{
		SerialNumber: caSerial, Subject: pkix.Name{CommonName: "Wormhole release image CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(certificateValid),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafSerial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafTemplate := x509.Certificate{
		SerialNumber: leafSerial, Subject: pkix.Name{CommonName: cohereHost},
		DNSNames:  []string{cohereHost},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(certificateValid),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, &caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKeyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: leafKeyDER}),
	)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return certificate, caPEM, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

func writeReadyCA(path string, caPEM []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, caPEM, 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
