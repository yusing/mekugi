package router

import "testing"

func TestMalformedShellAuthorDoesNotPersistAcrossTurns(t *testing.T) {
	for _, author := range []string{"/root/a\nspoof", "/root/a\rspoof", "/root/a\x00spoof"} {
		t.Run(author, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			proxy.commentaryEndpoint = "http://127.0.0.1:8080" + commentaryPublisherPath
			first, _ := prepareActivityTest(t, proxy, "first", "child", "parent", author, nil)
			first.Close()
			if len(proxy.commentary.threads) != 0 || len(proxy.commentary.routes) != 0 {
				t.Fatal("malformed author acquired persistent shell provenance")
			}
			if proxy.shellSessions[first.shellDirectory].commentary != nil {
				t.Fatal("malformed author installed a shell capability")
			}

			// Ancestry stays conflicted, but a later valid author may still use
			// local shell commentary without inheriting malformed provenance.
			next, _ := prepareActivityTest(t, proxy, "next", "child", "parent", "/root/valid", nil)
			provenance := proxy.commentary.threads["child"]
			if provenance == nil || provenance.author != "/root/valid" {
				t.Fatal("later valid author was not admitted")
			}
			token := proxy.commentary.subscribeThread(next.historySessionID, "child", "/root/valid")
			proxy.commentary.publish(token, "Local progress.", false)
			messages := proxy.commentary.drain(token)
			if len(messages) != 1 || messages[0].text != "[`/root/valid`] Local progress." {
				t.Fatal("later publication used malformed author", messages)
			}
		})
	}
}
