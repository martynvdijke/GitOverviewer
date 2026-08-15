package deploy_test

import (
	"context"
	"testing"

	"gitlens/internal/deploy"
)

// fakeDeployer implements deploy.Deployer for testing consumers.
type fakeDeployer struct {
	pullErr error
	calls   []struct {
		Target deploy.Target
		Tag    string
	}
}

func (f *fakeDeployer) PullAndUpdate(_ context.Context, target deploy.Target, tag string) (*deploy.DeployResult, error) {
	f.calls = append(f.calls, struct {
		Target deploy.Target
		Tag    string
	}{target, tag})
	return nil, f.pullErr
}

func TestFakeDeployer_Success(t *testing.T) {
	d := &fakeDeployer{}
	target := deploy.Target{Repository: "test/repo", Image: "img", Container: "c"}
	result, err := d.PullAndUpdate(context.Background(), target, "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if len(d.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(d.calls))
	}
	if d.calls[0].Tag != "1.0.0" {
		t.Fatalf("expected tag 1.0.0, got %s", d.calls[0].Tag)
	}
}

func TestFakeDeployer_Error(t *testing.T) {
	d := &fakeDeployer{pullErr: context.DeadlineExceeded}
	target := deploy.Target{Repository: "test/repo", Image: "img", Container: "c"}
	_, err := d.PullAndUpdate(context.Background(), target, "latest")
	if err == nil {
		t.Fatal("expected error")
	}
}

// TestNewDeployer ensures the factory returns a non-nil value.
func TestNewDeployer(t *testing.T) {
	d := deploy.NewDeployer()
	if d == nil {
		t.Fatal("expected non-nil deployer")
	}
}
