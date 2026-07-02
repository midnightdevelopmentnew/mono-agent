package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/rs/zerolog"
)

func main() {
	logger := zerolog.New(os.Stderr).Level(zerolog.WarnLevel)
	srv := extension.NewServer(":9222", logger)
	srv.StartAsync(context.Background())
	if err := srv.WaitForConnection(20 * time.Second); err != nil {
		fmt.Println("no connection:", err)
		os.Exit(1)
	}
	fmt.Println("connected")
	
	// Create tab
	tabID, err := srv.CreateTab("https://gemini.google.com/app")
	if err != nil {
		fmt.Println("create tab err:", err)
		os.Exit(1)
	}
	fmt.Println("tab:", tabID)
	
	page := extension.NewExtensionPage(srv, tabID)
	time.Sleep(6 * time.Second)

	// Type via EvalCDP into the input
	page.EvalCDP(`(function() {
		var inp = document.querySelector('div.ql-editor[contenteditable="true"], [role="textbox"]');
		if (inp) { inp.focus(); }
	})()`)
	time.Sleep(500 * time.Millisecond)
	// Send message via EvalCDP click on send
	page.EvalCDP(`(function() {
		var btn = document.querySelector('button[data-test-id="send-button"], button.send-button, button[aria-label*="Send"], mat-icon[fonticon="send"]');
		var txt = document.querySelector('div.ql-editor[contenteditable="true"], [role="textbox"]');
		if (txt) { txt.textContent = 'Hello, respond with "hi"'; txt.dispatchEvent(new Event('input',{bubbles:true})); }
		setTimeout(function() { if (btn) btn.click(); }, 1000);
	})()`)
	
	fmt.Println("waiting 15s for response...")
	time.Sleep(15 * time.Second)

	// Dump custom elements
	raw, _ := page.EvalCDP(`(function() {
		var counts = {};
		document.querySelectorAll('*').forEach(function(el) {
			var t = el.tagName.toLowerCase();
			if (t.includes('-')) counts[t] = (counts[t]||0)+1;
		});
		return JSON.stringify(counts);
	})()`)
	fmt.Println("custom elements:", raw)
	
	// Try common selectors
	for _, sel := range []string{
		"message-content","model-response","ms-chat-turn",
		"ms-prompt-response","response-container","bard-response",
		"[data-response-type]","[role='article']",".response-content",
		"conversation-container","response-chunk",
	} {
		n, _ := page.EvalCDP(fmt.Sprintf(`document.querySelectorAll('%s').length`, sel))
		if count, ok := n.(float64); ok && count > 0 {
			fmt.Printf("MATCH: %s = %d\n", sel, int(count))
		}
	}
}
