//go:build fuzz

package harness

import (
	"bytes"
	"testing"
)

func FuzzParseStreamLines(f *testing.F) {
	f.Add([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s1\"}\n"))
	f.Add([]byte("{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s1\",\"result\":\"done\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n"))
	f.Add([]byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n"))
	f.Add([]byte("not json at all\n"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, input []byte) {
		// Drain events in background so the channel never blocks.
		ch := make(chan Event, 4096)
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range ch {
			}
		}()

		_, _ = parseStreamLines(bytes.NewReader(input), ch)
		close(ch)
		<-done
	})
}
