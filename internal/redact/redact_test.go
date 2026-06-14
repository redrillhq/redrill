package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestRedactValues(t *testing.T) {
	t.Parallel()
	r := New("hunter2", "s3cr3t-key")
	out := r.Redact("user=admin password=hunter2 apikey=s3cr3t-key")
	if strings.Contains(out, "hunter2") || strings.Contains(out, "s3cr3t-key") {
		t.Fatalf("secret leaked: %q", out)
	}
	if !strings.Contains(out, Placeholder) {
		t.Fatalf("no placeholder in %q", out)
	}
	if !strings.Contains(out, "user=admin") {
		t.Errorf("non-secret text dropped: %q", out)
	}
}

func TestRedactMultiline(t *testing.T) {
	t.Parallel()
	r := New("topsecret")
	in := "line1: topsecret\nline2: fine\nline3: topsecret again"
	out := r.Redact(in)
	if strings.Contains(out, "topsecret") {
		t.Fatalf("secret survived on some line: %q", out)
	}
	if strings.Count(out, Placeholder) != 2 {
		t.Errorf("want 2 redactions across lines, got %q", out)
	}
}

func TestRedactInsideJSONAndQuotes(t *testing.T) {
	t.Parallel()
	r := New("hunter2")
	out := r.Redact(`{"db":{"password":"hunter2"},"note":"pw is hunter2"}`)
	if strings.Contains(out, "hunter2") {
		t.Fatalf("secret survived inside JSON/quotes: %q", out)
	}
	if want := `{"db":{"password":"` + Placeholder + `"},"note":"pw is ` + Placeholder + `"}`; out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestRedactLongestFirst(t *testing.T) {
	t.Parallel()
	r := New("abc", "abcdef")
	if out := r.Redact("abcdef"); out != Placeholder {
		t.Errorf("Redact(abcdef) = %q, want a single %q", out, Placeholder)
	}
}

func TestRedactEmptySecretIgnored(t *testing.T) {
	t.Parallel()
	r := New("", "   ")
	r.AddSecret("")
	const text = "nothing secret here"
	if out := r.Redact(text); out != text {
		t.Errorf("empty secret over-redacted: %q", out)
	}
}

func TestRedactNoSecrets(t *testing.T) {
	t.Parallel()
	var r Redactor
	const text = "plain output"
	if out := r.Redact(text); out != text {
		t.Errorf("zero-value Redactor changed output: %q", out)
	}
}

func TestRedactBytes(t *testing.T) {
	t.Parallel()
	r := New("hunter2")
	out := r.RedactBytes([]byte("x=hunter2"))
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("secret leaked: %q", out)
	}
	if string(out) != "x="+Placeholder {
		t.Errorf("got %q", out)
	}
}

func TestAddEnvScrubsSecretNamesOnly(t *testing.T) {
	t.Parallel()
	secretNames := []string{
		"POSTGRES_PASSWORD", "PGPASSWORD", "BORG_PASSPHRASE", "MYAPP_TOKEN",
		"AWS_SECRET_ACCESS_KEY", "B2_ACCOUNT_KEY", "API_KEY", "DB_PASSWD", "APP_CREDENTIAL",
	}
	for _, name := range secretNames {
		t.Run("secret/"+name, func(t *testing.T) {
			t.Parallel()
			r := New()
			r.AddEnv(name, "VALUEXYZ")
			if out := r.Redact("env " + name + "=VALUEXYZ"); strings.Contains(out, "VALUEXYZ") {
				t.Errorf("%s value not scrubbed: %q", name, out)
			}
		})
	}

	plainNames := []string{"PATH", "HOME", "PGHOST", "LANG", "MONKEY_MODE", "KEYBOARD_LAYOUT"}
	for _, name := range plainNames {
		t.Run("plain/"+name, func(t *testing.T) {
			t.Parallel()
			r := New()
			r.AddEnv(name, "VISIBLEVAL")
			if out := r.Redact("env " + name + "=VISIBLEVAL"); !strings.Contains(out, "VISIBLEVAL") {
				t.Errorf("%s value wrongly scrubbed: %q", name, out)
			}
		})
	}
}

func TestRedactConcurrent(t *testing.T) {
	t.Parallel()
	r := New("hunter2")
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%5 == 0 {
				r.AddSecret("extra")
			}
			if out := r.Redact("x=hunter2"); strings.Contains(out, "hunter2") {
				t.Errorf("secret leaked under concurrency: %q", out)
			}
		}(i)
	}
	wg.Wait()
}
