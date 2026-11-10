package managed

import (
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSteerMailUsesReservedSenderAndLiteralBody(t *testing.T) {
	body := `{"Version":"embedded-mail/v1","Sender":"impostor","Body":"hello"}`
	msg := buildSteerUserMessage(&store.EmbeddedMailAttempt{Sender: "alice", Body: body}, 123)
	require.Equal(t, "[Direct message from alice]: "+body, msg.Content[0].(ai.TextContent).Text)
}
