package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenRecoveringSerializesNewBuildAdmissionThroughCandidateCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	graphStore, err := Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.CreateCandidate(context.Background(), Revision{ProjectID: "project-a", ID: "stale", IndexedCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	otherDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other, err := Open(context.Background(), otherDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := afterRecoverInterruptedLifecycle
	afterRecoverInterruptedLifecycle = func() {
		close(entered)
		<-release
	}
	t.Cleanup(func() { afterRecoverInterruptedLifecycle = previous })
	recoveryDone := make(chan error, 1)
	go func() {
		_, err := OpenRecovering(context.Background(), db, "project-a")
		recoveryDone <- err
	}()
	<-entered
	buildDone := make(chan error, 1)
	go func() { buildDone <- other.BeginBuild(context.Background(), "new-live") }()
	select {
	case err := <-buildDone:
		t.Fatalf("build crossed startup recovery barrier early: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if err := <-buildDone; err != nil {
		t.Fatal(err)
	}
	if err := other.CreateCandidate(context.Background(), Revision{ProjectID: "project-a", ID: "new-live", IndexedCommit: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	revision, err := other.Revision(context.Background(), "new-live")
	if err != nil || revision.State != RevisionCandidate {
		t.Fatalf("newly admitted candidate = %+v, %v", revision, err)
	}
	_ = other.EndBuild(context.Background(), "new-live")
}

func TestOpenRecoveringSerializesDisableAdmissionThroughCandidateCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := Open(context.Background(), db, "project-a"); err != nil {
		t.Fatal(err)
	}
	otherDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	other, err := Open(context.Background(), otherDB, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := afterRecoverInterruptedLifecycle
	afterRecoverInterruptedLifecycle = func() { close(entered); <-release }
	t.Cleanup(func() { afterRecoverInterruptedLifecycle = previous })
	recoveryDone := make(chan error, 1)
	go func() { _, err := OpenRecovering(context.Background(), db, "project-a"); recoveryDone <- err }()
	<-entered
	disableDone := make(chan error, 1)
	go func() { disableDone <- other.Disable(context.Background()) }()
	select {
	case err := <-disableDone:
		t.Fatalf("disable crossed startup recovery barrier early: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}
}

func TestUnknownLeaseProbeDoesNotReclaimOccupiedBuild(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "gateway.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	graphStore, err := Open(context.Background(), db, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := graphStore.BeginBuild(context.Background(), "occupied"); err != nil {
		t.Fatal(err)
	}
	if err := graphStore.CreateCandidate(context.Background(), Revision{ProjectID: "project-a", ID: "occupied", IndexedCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	previous := processLeaseProbe
	processLeaseProbe = func(int, string) processLeaseStatus { return leaseUnknown }
	t.Cleanup(func() { processLeaseProbe = previous })
	if err := graphStore.BeginBuild(context.Background(), "replacement"); !errors.Is(err, ErrBuildInProgress) {
		t.Fatalf("BeginBuild with uncertain owner = %v, want ErrBuildInProgress", err)
	}
	if _, err := OpenRecovering(context.Background(), db, "project-a"); err != nil {
		t.Fatal(err)
	}
	revision, err := graphStore.Revision(context.Background(), "occupied")
	if err != nil || revision.State != RevisionCandidate {
		t.Fatalf("uncertain-owner candidate = %+v, %v", revision, err)
	}
}

func TestClassifyProcessLeaseFailsClosedOnUncertainProbe(t *testing.T) {
	for _, test := range []struct {
		name      string
		signalErr error
		actual    string
		startErr  error
		want      processLeaseStatus
	}{
		{name: "permission denied", signalErr: syscall.EPERM, want: leaseUnknown},
		{name: "identity unavailable", actual: "", startErr: errors.New("unavailable"), want: leaseUnknown},
		{name: "same process", actual: "proc:42", want: leaseLive},
		{name: "pid reused", actual: "proc:43", want: leaseDead},
		{name: "process absent", signalErr: syscall.ESRCH, want: leaseDead},
		{name: "process already reaped", signalErr: os.ErrProcessDone, want: leaseDead},
		{name: "pid fallback remains conservative", actual: "", startErr: errors.New("unavailable"), want: leaseLive},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := "proc:42"
			if test.name == "pid fallback remains conservative" {
				expected = "pid:123"
			}
			if got := classifyProcessLease(123, expected, test.signalErr, test.actual, test.startErr); got != test.want {
				t.Fatalf("classifyProcessLease() = %v, want %v", got, test.want)
			}
		})
	}
}
