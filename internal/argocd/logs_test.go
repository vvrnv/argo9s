package argocd

import "testing"

func TestParseLogLine(t *testing.T) {
	content, last, err := parseLogLine(`{"result":{"content":"hello","last":false}}`)
	if err != nil || content != "hello" || last {
		t.Fatalf("got %q last=%v err=%v", content, last, err)
	}
	content, last, err = parseLogLine(`{"content":"bye","last":true}`)
	if err != nil || content != "bye" || !last {
		t.Fatalf("got %q last=%v err=%v", content, last, err)
	}
}
