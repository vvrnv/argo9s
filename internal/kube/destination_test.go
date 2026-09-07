package kube

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestRestConfigFromClusterSecret(t *testing.T) {
	cfgJSON, _ := json.Marshal(map[string]any{
		"bearerToken": "tok-123",
		"tlsClientConfig": map[string]any{
			"insecure": false,
			"caData":   []byte("-----BEGIN CERT-----\nX\n-----END CERT-----\n"),
		},
	})
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "argocd"},
		Data: map[string][]byte{
			"name":   []byte("dest-a"),
			"server": []byte("https://dest.example:6443"),
			"config": cfgJSON,
		},
	}
	cfg, err := restConfigFromClusterSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "https://dest.example:6443" {
		t.Fatalf("host=%s", cfg.Host)
	}
	if cfg.BearerToken != "tok-123" {
		t.Fatalf("token=%s", cfg.BearerToken)
	}
	if len(cfg.CAData) == 0 {
		t.Fatal("expected CAData")
	}
}

func TestIsLocalDestination(t *testing.T) {
	hub := &Client{Config: &rest.Config{Host: "https://127.0.0.1:6443"}}
	if !isLocalDestination(hub, "") {
		t.Fatal("empty should be local")
	}
	if !isLocalDestination(hub, "https://kubernetes.default.svc") {
		t.Fatal("in-cluster should be local")
	}
	if !isLocalDestination(hub, "https://127.0.0.1:6443/") {
		t.Fatal("same host should be local")
	}
	if isLocalDestination(hub, "https://other.example:6443") {
		t.Fatal("other should not be local")
	}
}
