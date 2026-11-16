package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Respect process group signals (SIGTERM / SIGINT)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		os.Exit(143)
	}()

	args := os.Args[1:]

	// Check for version command or flag
	if len(args) > 0 {
		if args[0] == "--version" || args[0] == "version" || (len(args) == 1 && args[0] == "-v") {
			fmt.Println("agy version 1.5.9")
			os.Exit(0)
		}

		if args[0] == "models" {
			authEnv := os.Getenv("TEST_AGY_AUTH")
			if authEnv == "unauthenticated" {
				fmt.Fprintln(os.Stderr, "Fetching available models...")
				fmt.Fprintln(os.Stderr, "Error: Please sign in to view available models. Launch the CLI without arguments to sign in.")
				os.Exit(1)
			}
			fmt.Println("gemini-2.5-pro")
			fmt.Println("gemini-2.5-flash")
			fmt.Println("gemini-3.8-pro")
			fmt.Println("gemini-3.8-flash")
			os.Exit(0)
		}
	}

	// Parse flags for stream-json mode
	var (
		isPrint      bool
		inputFormat  string
		outputFormat string
		settingsPath string
		conversation string
		model        string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--print", "--prompt":
			isPrint = true
		case "--input-format":
			if i+1 < len(args) {
				inputFormat = args[i+1]
				i++
			}
		case "--output-format":
			if i+1 < len(args) {
				outputFormat = args[i+1]
				i++
			}
		case "--settings":
			if i+1 < len(args) {
				settingsPath = args[i+1]
				i++
			}
		case "--conversation":
			if i+1 < len(args) {
				conversation = args[i+1]
				i++
			}
		case "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case "--dangerously-skip-permissions":
			// flag acknowledged
		}
	}

	// If settingsPath is provided, verify it exists and is readable JSON
	if settingsPath != "" {
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read settings file %s: %v\n", settingsPath, err)
			os.Exit(1)
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			fmt.Fprintf(os.Stderr, "Error: settings file is invalid JSON: %v\n", err)
			os.Exit(1)
		}
	}

	// Stream-json mode
	if isPrint && inputFormat == "stream-json" && outputFormat == "stream-json" {
		runStreamJSON(conversation, model)
		return
	}

	// Default fallback
	fmt.Fprintf(os.Stderr, "fake_agy: unrecognized arguments: %v\n", args)
	os.Exit(2)
}

func runStreamJSON(sessionID, model string) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	// Emit initial system init event
	emitEvent(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": sessionID,
		"model":      model,
	})

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(line, &raw); err != nil {
			emitError("invalid json on stdin")
			continue
		}

		msgType, _ := raw["type"].(string)

		switch msgType {
		case "user":
			uuid, _ := raw["uuid"].(string)
			message, _ := raw["message"].(map[string]interface{})
			content := message["content"]

			// 1. Echo user message back
			emitEvent(map[string]interface{}{
				"type":       "user",
				"uuid":       uuid,
				"session_id": sessionID,
				"isReplay":   true,
				"message": map[string]interface{}{
					"role":    "user",
					"content": content,
				},
			})

			// 2. Emit thought event
			emitEvent(map[string]interface{}{
				"type":       "thought",
				"session_id": sessionID,
				"thought":    "Processing user input...",
			})

			// Check if content indicates a tool call request
			contentStr := fmt.Sprintf("%v", content)
			if contentStr == "call_tool" || (message["tool"] != nil) {
				emitEvent(map[string]interface{}{
					"type":       "tool_call",
					"session_id": sessionID,
					"tool": map[string]interface{}{
						"id":   "call-1",
						"name": "scratchpad",
						"input": map[string]interface{}{
							"action": "read",
						},
					},
				})
			}

			// 3. Emit assistant text chunk
			emitEvent(map[string]interface{}{
				"type":       "assistant",
				"session_id": sessionID,
				"message": map[string]interface{}{
					"role": "assistant",
					"content": []map[string]interface{}{
						{
							"type": "text",
							"text": fmt.Sprintf("Response to: %v", content),
						},
					},
				},
			})

			// 4. Emit turn completion
			emitEvent(map[string]interface{}{
				"type":       "result",
				"subtype":    "success",
				"session_id": sessionID,
			})

		case "control_request":
			reqID, _ := raw["request_id"].(string)
			req, _ := raw["request"].(map[string]interface{})
			subtype, _ := req["subtype"].(string)

			emitEvent(map[string]interface{}{
				"type": "control_response",
				"response": map[string]interface{}{
					"request_id": reqID,
					"subtype":    "success",
					"action":     subtype,
				},
			})

		default:
			emitError(fmt.Sprintf("unknown type: %s", msgType))
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "fake_agy stdin error: %v\n", err)
	}
}

func emitEvent(v map[string]interface{}) {
	data, _ := json.Marshal(v)
	os.Stdout.Write(data)
	os.Stdout.Write([]byte("\n"))
}

func emitError(msg string) {
	emitEvent(map[string]interface{}{
		"type":    "result",
		"subtype": "error_during_execution",
		"error":   msg,
	})
}
