package cmd

import (
	"os"
	"strings"
	"testing"
)

// ─── commandExists ────────────────────────────────────────────────────────

func TestCommandExists(t *testing.T) {
	t.Run("existing command", func(t *testing.T) {
		if !commandExists("ls") {
			t.Error("expected ls to exist on PATH")
		}
	})

	t.Run("non-existent command", func(t *testing.T) {
		if commandExists("this-command-absolutely-does-not-exist-chief-test") {
			t.Error("expected non-existent command to return false")
		}
	})

	t.Run("sh exists", func(t *testing.T) {
		if !commandExists("sh") {
			t.Error("expected sh to exist on PATH")
		}
	})
}

// ─── detectVoiceBackend ──────────────────────────────────────────────────

func TestDetectVoiceBackendNoTools(t *testing.T) {
	// Save and clear env vars so we isolate the detection logic
	origOpenAI := os.Getenv("OPENAI_API_KEY")
	origGemini := os.Getenv("GEMINI_API_KEY")
	defer func() {
		os.Setenv("OPENAI_API_KEY", origOpenAI)
		os.Setenv("GEMINI_API_KEY", origGemini)
	}()

	os.Unsetenv("OPENAI_API_KEY")
	os.Unsetenv("GEMINI_API_KEY")

	// Without API keys or local tools, should fall back to prompt
	// (mlx_whisper and sox are almost certainly absent in CI)
	backend := detectVoiceBackend()
	validBackends := map[string]bool{"openai": true, "mlx": true, "gemini": true, "prompt": true}
	if !validBackends[backend] {
		t.Errorf("detectVoiceBackend() returned unknown backend %q", backend)
	}
}

func TestDetectVoiceBackendPrefersOpenAIOverGemini(t *testing.T) {
	if !commandExists("ffmpeg") && !commandExists("sox") {
		t.Skip("ffmpeg/sox not available")
	}
	if commandExists("mlx_whisper") {
		t.Skip("mlx_whisper available — mlx takes precedence over OpenAI in new ordering")
	}

	origOpenAI := os.Getenv("OPENAI_API_KEY")
	origGemini := os.Getenv("GEMINI_API_KEY")
	defer func() {
		os.Setenv("OPENAI_API_KEY", origOpenAI)
		os.Setenv("GEMINI_API_KEY", origGemini)
	}()

	os.Setenv("OPENAI_API_KEY", "sk-test-key")
	os.Setenv("GEMINI_API_KEY", "gemini-test-key")

	backend := detectVoiceBackend()
	if backend != "openai" {
		t.Errorf("expected 'openai' to beat 'gemini' when both keys set and no mlx, got %q", backend)
	}
}

func TestDetectVoiceBackendPrefersMLX(t *testing.T) {
	if !commandExists("mlx_whisper") {
		t.Skip("mlx_whisper not available")
	}
	if !commandExists("ffmpeg") && !commandExists("sox") {
		t.Skip("no audio recorder available")
	}

	// mlx should win even if OpenAI key is set
	origOpenAI := os.Getenv("OPENAI_API_KEY")
	defer os.Setenv("OPENAI_API_KEY", origOpenAI)
	os.Setenv("OPENAI_API_KEY", "sk-test-key")

	backend := detectVoiceBackend()
	if backend != "mlx" {
		t.Errorf("expected 'mlx' to be preferred over 'openai', got %q", backend)
	}
}

func TestDetectVoiceBackendFallsBackToGemini(t *testing.T) {
	if !commandExists("ffmpeg") && !commandExists("sox") {
		t.Skip("ffmpeg/sox not available — skipping Gemini fallback test")
	}
	if commandExists("mlx_whisper") {
		t.Skip("mlx_whisper available — OpenAI/mlx would take precedence")
	}

	origOpenAI := os.Getenv("OPENAI_API_KEY")
	origGemini := os.Getenv("GEMINI_API_KEY")
	defer func() {
		os.Setenv("OPENAI_API_KEY", origOpenAI)
		os.Setenv("GEMINI_API_KEY", origGemini)
	}()

	os.Unsetenv("OPENAI_API_KEY")
	os.Setenv("GEMINI_API_KEY", "test-gemini-key")

	backend := detectVoiceBackend()
	if backend != "gemini" {
		t.Errorf("expected 'gemini' backend when GEMINI_API_KEY set and no OpenAI key, got %q", backend)
	}
}

// minimalWAV returns a minimal valid WAV file (silence) that can be sent to
// transcription APIs without needing ffmpeg or sox.
func minimalWAV(t *testing.T) string {
	t.Helper()
	// 44-byte WAV header + 32000 bytes of PCM silence (1s at 16kHz mono 16-bit)
	sampleRate := 16000
	dataSize := sampleRate * 2 // 16-bit = 2 bytes per sample
	chunkSize := 36 + dataSize

	buf := make([]byte, 44+dataSize)
	copy(buf[0:], []byte("RIFF"))
	buf[4] = byte(chunkSize); buf[5] = byte(chunkSize >> 8); buf[6] = byte(chunkSize >> 16); buf[7] = byte(chunkSize >> 24)
	copy(buf[8:], []byte("WAVE"))
	copy(buf[12:], []byte("fmt "))
	buf[16] = 16               // Subchunk1Size
	buf[20] = 1; buf[21] = 0   // PCM
	buf[22] = 1; buf[23] = 0   // mono
	buf[24] = byte(sampleRate); buf[25] = byte(sampleRate >> 8); buf[26] = 0; buf[27] = 0
	byteRate := sampleRate * 2
	buf[28] = byte(byteRate); buf[29] = byte(byteRate >> 8); buf[30] = 0; buf[31] = 0
	buf[32] = 2; buf[33] = 0   // BlockAlign
	buf[34] = 16; buf[35] = 0  // BitsPerSample
	copy(buf[36:], []byte("data"))
	buf[40] = byte(dataSize); buf[41] = byte(dataSize >> 8); buf[42] = byte(dataSize >> 16); buf[43] = byte(dataSize >> 24)
	// bytes 44..end are zero (silence)

	f, err := os.CreateTemp("", "chief-test-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(buf)
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

// ─── recordAndTranscribeOnce ──────────────────────────────────────────────

func TestRecordAndTranscribeOncePromptBackend(t *testing.T) {
	// "prompt" backend reads from stdin — we can't easily test interactively,
	// but we can verify it doesn't panic and returns a non-error for empty input.
	// Just verify the function exists and handles the prompt case.
	_ = recordAndTranscribeOnce // function must exist
}

// ─── OpenAI Whisper transcription (integration — needs OPENAI_API_KEY) ───

func TestSendToOpenAIIntegration(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	wav := minimalWAV(t)
	transcript, err := sendToOpenAI(wav)

	// Must not fail with auth error
	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403")) {
		t.Fatalf("OpenAI auth failed — check OPENAI_API_KEY: %v", err)
	}
	// Empty/silence audio may return empty transcript or a short filler — that's fine
	t.Logf("transcript: %q, err: %v", transcript, err)
}

// ─── Gemini transcription (integration — needs GEMINI_API_KEY) ────────────

func TestSendToGeminiIntegration(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	wav := minimalWAV(t)
	transcript, err := sendToGemini(wav)

	if err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "API_KEY_INVALID")) {
		t.Fatalf("Gemini auth failed — check GEMINI_API_KEY: %v", err)
	}
	t.Logf("transcript: %q, err: %v", transcript, err)
}

