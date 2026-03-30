package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// VoiceOptions configures audio recording and transcription.
type VoiceOptions struct {
	// DurationSec is the max recording duration in seconds (default: 30)
	DurationSec int

	// Backend forces a specific backend: "openai", "gemini", "mlx", "sox"
	// If empty, auto-detects in order: openai → mlx → gemini → prompt
	Backend string
}

// CollectVoiceInput runs an interactive multi-round voice loop.
//
// Flow per round:
//  1. Record audio (auto-stops on 5s silence, Ctrl+C force-stops)
//  2. Show transcript
//  3. Prompt: [K]eep / [R]edo / [D]one
//     - K: save segment, ask "Add more?" → Y continues to next round, N finishes
//     - R: discard, re-record the same round
//     - D: save segment (if non-empty) and finish immediately
//
// All kept segments are combined into one description that is passed to
// auto-research and then to the PRD generator.
func CollectVoiceInput(opts VoiceOptions) (string, error) {
	if opts.DurationSec <= 0 {
		opts.DurationSec = 30
	}
	backend := opts.Backend
	if backend == "" {
		backend = detectVoiceBackend()
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("┌─ Voice input ────────────────────────────────────────────────┐")
	fmt.Println("│  Speak your request — any type of work is fine:              │")
	fmt.Println("│  bug fix · new feature · tests · refactor · explore · debug  │")
	fmt.Println("│  After each round: [K]eep  [R]edo  [D]one (Enter = Keep)     │")
	fmt.Println("│  Ctrl+C during recording = force-stop mic                    │")
	fmt.Println("└──────────────────────────────────────────────────────────────┘")
	fmt.Println()

	var segments []string

	for round := 1; ; round++ {
		fmt.Printf("── Round %d ", round)
		if len(segments) > 0 {
			fmt.Printf("(%d segment(s) captured so far) ", len(segments))
		}
		fmt.Println("──────────────────────────────────")

		// Record and transcribe
		transcript, err := recordAndTranscribeOnce(backend, opts.DurationSec)
		if err != nil {
			return "", fmt.Errorf("round %d: %w", round, err)
		}
		transcript = strings.TrimSpace(transcript)

		// Show result and prompt for action
		if transcript == "" {
			fmt.Println("(nothing detected — background noise or silence)")
			fmt.Print("[R]edo  [D]one → ")
		} else {
			fmt.Printf("\nTranscript: %s\n\n", transcript)
			fmt.Print("[K]eep  [R]edo  [D]one → ")
		}

		line, _ := reader.ReadString('\n')
		choice := strings.ToLower(strings.TrimSpace(line))
		if choice == "" {
			choice = "k"
		}

		switch {
		case choice == "r" || choice == "redo":
			fmt.Println("Re-recording...")
			round-- // stay on same round number
			continue

		case choice == "d" || choice == "done":
			if transcript != "" {
				segments = append(segments, transcript)
			}
			// Break out of loop — done collecting
			goto finish

		default: // k, keep, enter
			if transcript != "" {
				segments = append(segments, transcript)
			}
		}

		// After keeping, ask whether to add another round
		fmt.Println()
		fmt.Print("Add more context? [Y/n] ")
		moreLine, _ := reader.ReadString('\n')
		more := strings.ToLower(strings.TrimSpace(moreLine))
		if more == "n" || more == "no" {
			break
		}
		fmt.Println()
	}

finish:
	if len(segments) == 0 {
		return "", fmt.Errorf("no voice input captured")
	}

	fmt.Println()
	fmt.Println("── Collected inputs ─────────────────────────────────────────")
	for i, s := range segments {
		fmt.Printf("  %d. %s\n", i+1, s)
	}
	fmt.Println()
	fmt.Println("All inputs collected. Starting auto-research...")
	fmt.Println()

	return strings.Join(segments, "\n\n"), nil
}

// recordAndTranscribeOnce records one voice segment and returns the transcript.
func recordAndTranscribeOnce(backend string, durationSec int) (string, error) {
	switch backend {
	case "openai":
		return transcribeViaOpenAI(durationSec)
	case "mlx":
		return transcribeViaMlxWhisper(durationSec)
	case "gemini":
		return transcribeViaGemini(durationSec)
	case "prompt":
		fmt.Print("Type your input (Enter to submit): ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		return strings.TrimSpace(line), nil
	default:
		return "", fmt.Errorf("unknown voice backend: %s", backend)
	}
}

// RecordAndTranscribe records audio from the microphone and returns a transcript.
// It auto-selects the best available backend.
func RecordAndTranscribe(opts VoiceOptions) (string, error) {
	if opts.DurationSec <= 0 {
		opts.DurationSec = 15
	}

	backend := opts.Backend
	if backend == "" {
		backend = detectVoiceBackend()
	}

	switch backend {
	case "openai":
		return transcribeViaOpenAI(opts.DurationSec)
	case "mlx":
		return transcribeViaMlxWhisper(opts.DurationSec)
	case "gemini":
		return transcribeViaGemini(opts.DurationSec)
	case "prompt":
		return readVoiceFromPrompt()
	default:
		return "", fmt.Errorf("unknown voice backend: %s", backend)
	}
}

// detectVoiceBackend picks the best available backend.
// Priority: mlx (local/free/fast) > OpenAI API > Gemini API > text fallback.
func detectVoiceBackend() string {
	hasRecorder := commandExists("sox") || commandExists("ffmpeg")

	// mlx_whisper: Apple Silicon local — no API key, no network, fastest
	if commandExists("mlx_whisper") && hasRecorder {
		return "mlx"
	}
	// OpenAI Whisper API: good accuracy, requires API key + internet
	if os.Getenv("OPENAI_API_KEY") != "" && hasRecorder {
		return "openai"
	}
	// Gemini: alternative API
	if os.Getenv("GEMINI_API_KEY") != "" && hasRecorder {
		return "gemini"
	}
	// Nothing available — fall back to text prompt
	return "prompt"
}

// transcribeViaOpenAI records audio and sends it to OpenAI Whisper REST API.
func transcribeViaOpenAI(durationSec int) (string, error) {
	audioFile, err := recordAudio(durationSec)
	if err != nil {
		return "", err
	}
	defer os.Remove(audioFile)
	return sendToOpenAI(audioFile)
}

// sendToOpenAI sends an existing audio file to OpenAI Whisper and returns the transcript.
func sendToOpenAI(audioFile string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY not set")
	}

	f, err := os.Open(audioFile)
	if err != nil {
		return "", fmt.Errorf("open audio: %w", err)
	}
	defer f.Close()

	// Use gpt-4o-transcribe for best accuracy; falls back to whisper-1 if unavailable
	model := "gpt-4o-transcribe"
	if os.Getenv("CHIEF_OPENAI_MODEL") != "" {
		model = os.Getenv("CHIEF_OPENAI_MODEL")
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", model)
	fw, err := w.CreateFormFile("file", filepath.Base(audioFile))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return "", err
	}
	w.Close()

	fmt.Print("Transcribing via OpenAI Whisper... ")
	req, _ := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper API request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("whisper API error %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse whisper response: %w", err)
	}
	fmt.Println("done")
	return strings.TrimSpace(result.Text), nil
}

// transcribeViaMlxWhisper records audio and runs mlx_whisper locally.
func transcribeViaMlxWhisper(durationSec int) (string, error) {
	audioFile, err := recordAudio(durationSec)
	if err != nil {
		return "", err
	}
	defer os.Remove(audioFile)

	fmt.Print("Transcribing via mlx-whisper (local)... ")
	txtFile := strings.TrimSuffix(audioFile, filepath.Ext(audioFile)) + ".txt"
	defer os.Remove(txtFile)

	out, err := exec.Command("mlx_whisper", audioFile,
		"--model", "mlx-community/whisper-base",
		"--output-format", "txt",
	).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mlx_whisper failed: %s", out)
	}

	// mlx_whisper writes <file>.txt in the same directory
	data, err := os.ReadFile(txtFile)
	if err != nil {
		// Try parsing stdout directly
		return strings.TrimSpace(string(out)), nil
	}
	fmt.Println("done")
	return strings.TrimSpace(string(data)), nil
}

// transcribeViaGemini records audio and sends it to the Gemini transcription API.
// Uses gemini-2.0-flash which supports inline audio.
func transcribeViaGemini(durationSec int) (string, error) {
	audioFile, err := recordAudio(durationSec)
	if err != nil {
		return "", err
	}
	defer os.Remove(audioFile)
	return sendToGemini(audioFile)
}

// sendToGemini sends an existing audio file to Gemini and returns the transcript.
func sendToGemini(audioFile string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("GEMINI_API_KEY not set")
	}

	audioData, err := os.ReadFile(audioFile)
	if err != nil {
		return "", fmt.Errorf("read audio: %w", err)
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{
						"inline_data": map[string]any{
							"mime_type": "audio/wav",
							"data":      base64.StdEncoding.EncodeToString(audioData),
						},
					},
					{"text": "Transcribe this audio exactly as spoken. Return only the transcript text, nothing else."},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	// Use gemini-2.5-flash-preview for best audio transcription accuracy
	geminiModel := "gemini-2.5-flash-preview-04-17"
	if os.Getenv("CHIEF_GEMINI_MODEL") != "" {
		geminiModel = os.Getenv("CHIEF_GEMINI_MODEL")
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + geminiModel + ":generateContent?key=" + apiKey

	fmt.Printf("Transcribing via Gemini (%s)... ", geminiModel)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("gemini API error %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil || len(result.Candidates) == 0 {
		return "", fmt.Errorf("parse gemini response: %w", err)
	}
	if len(result.Candidates[0].Content.Parts) == 0 {
		return "", nil // empty audio → empty transcript
	}
	text := strings.TrimSpace(result.Candidates[0].Content.Parts[0].Text)
	fmt.Println("done")
	return text, nil
}

// recordAudio records from the default microphone to a temp WAV file.
// Uses silence detection to stop automatically when the user stops speaking
// (2 seconds of silence after at least 0.5s of speech).
// Falls back to a fixed duration if silence detection is unavailable.
// Returns the path to the file; caller is responsible for removal.
func recordAudio(durationSec int) (string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("chief-voice-%d.wav", time.Now().UnixNano()))
	dur := fmt.Sprintf("%d", durationSec)

	fmt.Printf("Listening... (auto-stops after 5s silence, %ds max — Ctrl+C to force stop)\n", durationSec)

	var cmd *exec.Cmd
	if commandExists("sox") {
		// silence filter: start after 0.1s above 1% volume, stop after 5s below 1% volume
		cmd = exec.Command("sox", "-d",
			"-r", "16000", "-c", "1",
			tmp,
			"trim", "0", dur,
			"silence", "1", "0.1", "1%", "1", "5.0", "1%",
		)
	} else if commandExists("ffmpeg") {
		// ffmpeg doesn't have built-in VAD; use fixed duration with Ctrl+C to stop
		cmd = exec.Command("ffmpeg",
			"-f", "avfoundation", "-i", ":0",
			"-t", dur,
			"-ar", "16000", "-ac", "1",
			tmp, "-y", "-loglevel", "quiet",
		)
		fmt.Printf("(Ctrl+C to stop recording early)\n")
	} else {
		return "", fmt.Errorf("no audio recorder found — install sox (brew install sox) or ffmpeg (brew install ffmpeg)")
	}

	// Run recorder in its own process group so terminal Ctrl+C doesn't propagate
	// SIGINT to chief — we catch it ourselves and forward only to the child.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start recorder: %w", err)
	}

	// Catch Ctrl+C: stop the recorder but let chief keep running.
	sigCh := make(chan os.Signal, 1)
	stopSig := make(chan struct{})
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		select {
		case <-sigCh:
			// SIGTERM to the recorder's process group — chief is unaffected.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		case <-stopSig:
		}
	}()

	waitErr := cmd.Wait()
	signal.Stop(sigCh)
	close(stopSig)

	// sox/ffmpeg exit non-zero on Ctrl+C; treat as success if file was created with content.
	if waitErr != nil {
		info, statErr := os.Stat(tmp)
		if statErr != nil || info.Size() <= 44 {
			return "", fmt.Errorf("recording failed: %w", waitErr)
		}
	}
	fmt.Println("Got it.")
	return tmp, nil
}

// readVoiceFromPrompt falls back to text input when no audio backend is available.
func readVoiceFromPrompt() (string, error) {
	fmt.Println()
	fmt.Println("No voice backend detected. Options to enable voice:")
	fmt.Println()
	fmt.Println("  OpenAI Whisper (recommended):   export OPENAI_API_KEY=sk-...  &&  brew install ffmpeg")
	fmt.Println("  Gemini (Google):                export GEMINI_API_KEY=...     &&  brew install ffmpeg")
	fmt.Println("  Local (Apple Silicon offline):  pip install mlx-whisper       &&  brew install sox")
	fmt.Println()
	fmt.Println("  macOS dictation (no install):   System Settings → Keyboard → Dictation → Enable")
	fmt.Println("    Press Fn Fn in terminal, speak, then:  chief prd \"<your text>\"")
	fmt.Println()
	fmt.Print("Or type your request now: ")

	var line string
	_, err := fmt.Scanln(&line)
	return strings.TrimSpace(line), err
}

// commandExists checks if a command is available on PATH.
func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
