package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/ybouhjira/claude-code-tts/internal/audio"
	"github.com/ybouhjira/claude-code-tts/internal/botcontrol"
	"github.com/ybouhjira/claude-code-tts/internal/cost"
	"github.com/ybouhjira/claude-code-tts/internal/logging"
	"github.com/ybouhjira/claude-code-tts/internal/ttsconfig"
	"github.com/ybouhjira/claude-code-tts/internal/voicemode"
)

// Server wraps the MCP server and worker pool
type Server struct {
	mcpServer  *server.MCPServer
	workerPool *WorkerPool
	modeStore  *voicemode.Store
	pollerStop context.CancelFunc // nil when no poller
}

// New creates a new TTS MCP server
func New() (*Server, error) {
	logging.Info("Creating TTS MCP server...")

	reg := ttsconfig.LoadOrDefault()
	player := audio.NewPlayer()
	modeStore := voicemode.DefaultStore()
	settingsStore := voicemode.DefaultSettingsStore()
	tgSender, tgReason := reg.TelegramSender()

	wp := NewWorkerPool(reg, player, 2, 50).
		WithMode(modeStore).
		WithSettings(settingsStore)
	// Keep this if/else: do NOT collapse it to WithTelegram(tgSender, tgReason).
	// tgSender is a *telegram.Sender, so when it's nil, passing it through the
	// telegramSender interface yields a non-nil typed-nil interface and
	// wp.telegram != nil would wrongly be true. The else branch passes a literal
	// nil so the "telegram unavailable" checks work.
	if tgSender != nil {
		wp.WithTelegram(tgSender, "")
	} else {
		wp.WithTelegram(nil, tgReason)
	}
	wp.Start()
	logging.Info("Worker pool created and started")

	mcpSrv := server.NewMCPServer("claude-code-tts", "1.0.0", server.WithToolCapabilities(true))
	s := &Server{mcpServer: mcpSrv, workerPool: wp, modeStore: modeStore}

	// Start the Telegram control poller when configured + the chat id parses.
	if tgSender != nil {
		if chatID, err := strconv.ParseInt(reg.TelegramChatID(), 10, 64); err == nil && chatID != 0 {
			ctx, cancel := context.WithCancel(context.Background())
			s.pollerStop = cancel
			poller := botcontrol.NewPoller(tgSender, settingsStore, &registrySource{reg: reg}, chatID)
			go poller.Run(ctx)
			logging.Info("Telegram control poller started")
		} else {
			logging.Info("Telegram poller not started: chat_id missing/invalid")
		}
	}

	s.registerTools()
	return s, nil
}

// registerTools adds the TTS tools to the MCP server
func (s *Server) registerTools() {
	// speak tool - converts text to speech
	speakTool := mcp.NewTool("speak",
		mcp.WithDescription("Convert text to speech and play it aloud. Use this to provide audio feedback to the user."),
		mcp.WithString("text", mcp.Required(),
			mcp.Description("The text to convert to speech (max 4096 characters)")),
		mcp.WithString("profile",
			mcp.Description("Named voice profile from config (e.g. default, error). Defaults to the configured default profile.")),
		mcp.WithString("provider",
			mcp.Description("Use an explicit provider (openai, grok, piper) instead of a profile.")),
		mcp.WithString("voice",
			mcp.Description("Override the profile's voice (provider-specific id, e.g. alloy/onyx for OpenAI or eve/leo for Grok).")),
		mcp.WithNumber("speed",
			mcp.Description("Override speech speed (provider-dependent range, e.g. 0.7-1.5).")),
	)

	s.mcpServer.AddTool(speakTool, s.handleSpeak)

	// tts_status tool - returns worker pool status
	statusTool := mcp.NewTool("tts_status",
		mcp.WithDescription("Get the current status of the TTS system including queue size, processed count, and recent jobs."),
	)

	s.mcpServer.AddTool(statusTool, s.handleStatus)

	// tts_pause tool - pauses job processing
	pauseTool := mcp.NewTool("tts_pause",
		mcp.WithDescription("Pause TTS processing. Queued jobs will wait until resumed."),
	)

	s.mcpServer.AddTool(pauseTool, s.handlePause)

	// tts_resume tool - resumes job processing
	resumeTool := mcp.NewTool("tts_resume",
		mcp.WithDescription("Resume TTS processing after pause."),
	)

	s.mcpServer.AddTool(resumeTool, s.handleResume)

	// tts_clear tool - clears pending jobs
	clearTool := mcp.NewTool("tts_clear",
		mcp.WithDescription("Clear all pending TTS jobs from the queue."),
	)

	s.mcpServer.AddTool(clearTool, s.handleClear)

	// tts_output tool - sets the voice output mode
	outputTool := mcp.NewTool("tts_output",
		mcp.WithDescription("Set where Claude's voice goes. Call this when the user asks to turn the voice on/off or change where it plays — e.g. \"turn the voice off\", \"speak out loud\", \"send the voice to my phone\", \"use both\"."),
		mcp.WithString("mode", mcp.Required(),
			mcp.Description("One of: off (silent), computer (this PC's speakers), phone (Telegram), both.")),
	)
	s.mcpServer.AddTool(outputTool, s.handleSetOutput)
}

// handleSpeak processes speak tool calls
func (s *Server) handleSpeak(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	text, ok := request.Params.Arguments["text"].(string)
	if !ok || text == "" {
		return mcp.NewToolResultError("text parameter is required"), nil
	}
	if len(text) > 4096 {
		return mcp.NewToolResultError("text exceeds maximum length of 4096 characters"), nil
	}

	str := func(k string) string {
		if v, ok := request.Params.Arguments[k].(string); ok {
			return v
		}
		return ""
	}
	profile, provider, voice := str("profile"), str("provider"), str("voice")
	var speed float64
	if v, ok := request.Params.Arguments["speed"].(float64); ok {
		speed = v
	}

	// Leave profile/provider empty when not given: the worker then resolves via
	// the registry Default() path, which honors CLAUDE_TTS_* env overrides and
	// the configured default profile.
	job, err := s.workerPool.Submit(SpeakRequest{
		Text: text, Profile: profile, Provider: provider, Voice: voice, Speed: speed,
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to queue TTS job: %v", err)), nil
	}
	sel := "default"
	if provider != "" {
		sel = "provider:" + provider
	} else if profile != "" {
		sel = profile
	}
	return mcp.NewToolResultText(fmt.Sprintf("TTS job queued (ID: %s, %s)", job.ID, sel)), nil
}

// handleStatus processes tts_status tool calls
func (s *Server) handleStatus(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logging.Debug("Received tts_status tool call")
	status := s.workerPool.GetStatus()

	jsonData, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		logging.Error("tts_status: failed to marshal: %v", err)
		return mcp.NewToolResultError(fmt.Sprintf("failed to marshal status: %v", err)), nil
	}

	logging.Debug("tts_status: processed=%d, failed=%d, pending=%d",
		status.TotalProcessed, status.TotalFailed, status.QueuePending)
	return mcp.NewToolResultText(string(jsonData)), nil
}

// handleSetOutput processes tts_output tool calls
func (s *Server) handleSetOutput(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m, _ := request.Params.Arguments["mode"].(string)
	mode := voicemode.Mode(m)
	if !voicemode.Valid(mode) {
		return mcp.NewToolResultError("invalid mode; use one of: off, computer, phone, both"), nil
	}
	if err := s.modeStore.Set(mode); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to set voice output: %v", err)), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Voice output set to: %s", mode)), nil
}

// handlePause processes tts_pause tool calls
func (s *Server) handlePause(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logging.Debug("Received tts_pause tool call")
	s.workerPool.Pause()
	return mcp.NewToolResultText("TTS processing paused. Queued jobs will wait until resumed."), nil
}

// handleResume processes tts_resume tool calls
func (s *Server) handleResume(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logging.Debug("Received tts_resume tool call")
	s.workerPool.Resume()
	return mcp.NewToolResultText("TTS processing resumed. Queued jobs will now be processed."), nil
}

// handleClear processes tts_clear tool calls
func (s *Server) handleClear(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	logging.Debug("Received tts_clear tool call")
	cleared := s.workerPool.Clear()
	return mcp.NewToolResultText(fmt.Sprintf("Cleared %d pending jobs from the queue.", cleared)), nil
}

// Start begins serving MCP requests via stdio
func (s *Server) Start() error {
	logging.Info("Starting stdio server (blocking)...")
	err := server.ServeStdio(s.mcpServer)
	if err != nil {
		logging.Error("ServeStdio returned error: %v", err)
	} else {
		logging.Info("ServeStdio returned without error")
	}
	return err
}

// Shutdown gracefully stops the server
func (s *Server) Shutdown() {
	logging.Info("Server shutdown initiated...")
	if s.pollerStop != nil {
		s.pollerStop()
	}
	s.workerPool.Stop()
	logging.Info("Server shutdown complete")
}

// registrySource adapts the ttsconfig registry to botcontrol.voiceModelSource:
// it reports the current provider's voices/models and synthesizes demo clips.
type registrySource struct{ reg *ttsconfig.Registry }

func (s *registrySource) Voices() []string {
	prov, _, err := s.reg.Default()
	if err != nil {
		return nil
	}
	return prov.Voices()
}

func (s *registrySource) Models() []string {
	prov, _, err := s.reg.Default()
	if err != nil {
		return nil
	}
	return cost.ModelsFor(prov.Name())
}

func (s *registrySource) Demo(ctx context.Context, voice string) ([]byte, string, error) {
	prov, req, err := s.reg.Default()
	if err != nil {
		return nil, "", err
	}
	req.Voice = voice
	req.Text = "Hi, this is the " + voice + " voice."
	req.Format = "opus"
	a, err := prov.Synthesize(ctx, req)
	if err != nil {
		return nil, "", err
	}
	return a.Data, a.Format, nil
}
