package terminal

import (
	"strings"
	"testing"
)

func TestCodecANSIFragmentsEraseAndPromptVariants(t *testing.T) {
	for _, prompt := range []string{"[admin@CHR] > ", "[admin@CHR] /ip/route>", "[admin@CHR] /ip/route<SAFE>", "[admin@CHR] <SAFE> > "} {
		d := newDecoder(512)
		last := event{}
		wire := "discarded\r\x1b[2K\x1b]0;private-window-title\a\x1b[32m" + prompt + "\x1b[0m"
		for _, b := range []byte(wire) {
			events, err := d.feed([]byte{b})
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if e.kind == promptEvent {
					last = e
				}
			}
		}
		if last.kind != promptEvent || last.safe != strings.Contains(prompt, "<SAFE>") || strings.Contains(last.text, "private-window-title") || strings.Contains(last.text, "discarded") {
			t.Fatalf("prompt decoding mismatch: %+v", last)
		}
	}
}
func TestCodecRejectsUnknownOrUnboundedControls(t *testing.T) {
	for _, wire := range []string{"\x00", "\x1b[3A", "\x1b[999999C", "\x1b]" + strings.Repeat("s", 513), "\x1b[" + strings.Repeat("1", 65)} {
		d := newDecoder(256)
		if _, err := d.feed([]byte(wire)); err == nil {
			t.Fatal("unrecognized control accepted")
		}
	}
}
func TestCodecDoesNotRepeatOnePagerOrClassifyHotlockAsReady(t *testing.T) {
	d := newDecoder(256)
	all := []event{}
	for _, chunk := range []string{"-- [Q quit|D dump|down]", " ", "\r\n"} {
		events, err := d.feed([]byte(chunk))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, events...)
	}
	pages := 0
	for _, e := range all {
		if e.kind == pageEvent {
			pages++
		}
	}
	if pages != 1 {
		t.Fatal("pager advanced more than once", pages)
	}
	if kind, _ := classify("[admin@CHR] >>"); kind == promptEvent {
		t.Fatal("hotlock would autocomplete fixed commands")
	}
}

func TestNativeCursorSaveRestoreAndResetSequence(t *testing.T) {
	d := newDecoder(512)
	if events, err := d.feed([]byte("ab\x1b7c\x1b8Z\r\n")); err != nil || len(events) != 1 || events[0].text != "abZ" {
		t.Fatal("cursor restore did not overwrite at the saved column", err)
	}
	var last event
	for _, b := range []byte("old protected prompt\r\x1b7\x1b8\x1bcMikroTik 7.24.6 (long-term)\r\r\nCHR Login:") {
		events, err := d.feed([]byte{b})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.kind == loginEvent {
				last = e
			}
		}
	}
	if last.kind != loginEvent || last.text != "CHR Login:" {
		t.Fatal("RIS failed to clear prior prompt presentation")
	}
	if _, err := d.feed([]byte("\x1b8")); codeOf(err) != ProtocolUnknown {
		t.Fatal("RIS retained untrusted saved cursor")
	}
}

func TestNative7246BottomMovementOnlyAtCleanLineBoundary(t *testing.T) {
	for _, prefix := range []string{"\r\r\r", "discarded\r\x1b[2K", "completed line\r\n"} {
		d := newDecoder(512)
		var prompt event
		for _, b := range []byte(prefix + "\x1b[9999B[admin@CHR] > ") {
			events, err := d.feed([]byte{b})
			if err != nil {
				t.Fatal("native clean-boundary movement rejected", err)
			}
			for _, e := range events {
				if e.kind == promptEvent {
					prompt = e
				}
			}
		}
		if prompt.kind != promptEvent || prompt.safe || strings.TrimSpace(prompt.text) != "[admin@CHR] >" {
			t.Fatal("native prompt not recovered")
		}
	}
	for _, prefix := range []string{"MD_BEGIN_abcdef", "MD_END_abcdef", "[admin@CHR] > ", "private pending input"} {
		d := newDecoder(512)
		if _, err := d.feed([]byte(prefix + "\r\r\r\x1b[9999B")); codeOf(err) != ProtocolUnknown {
			t.Fatal("visual movement discarded uncommitted content or marker prefix", err)
		}
	}
	for _, sequence := range []string{"\x1b[9998B", "\x1b[9999A", "\x1b[9999;1B"} {
		if _, err := newDecoder(512).feed([]byte(sequence)); codeOf(err) != ProtocolUnknown {
			t.Fatal("native exception expanded to other cursor movement", err)
		}
	}
}

func TestNative7246HomeClearLogoutDoesNotFlushPendingMarkers(t *testing.T) {
	d := newDecoder(512)
	if _, err := d.feed([]byte("[admin@CHR] <SAFE> ")); err != nil {
		t.Fatal(err)
	}
	var events []event
	for _, b := range []byte("\r\x1b7\x1b8\x1b[1;1H\x1b[H\x1b[J\x1bcMikroTik 7.24.6 (long-term)\r\r\nCHR Login:") {
		got, err := d.feed([]byte{b})
		if err != nil {
			t.Fatal("native logout tail rejected", err)
		}
		events = append(events, got...)
	}
	login := false
	for _, e := range events {
		login = login || e.kind == loginEvent
		if e.kind == promptEvent {
			t.Fatal("logout redraw emitted an authenticated prompt")
		}
	}
	if !login {
		t.Fatal("native login prompt lost")
	}
	for _, home := range []string{"\x1b[H", "\x1b[1;1H"} {
		for _, clearScreen := range []string{"\x1b[J", "\x1b[0J"} {
			d = newDecoder(512)
			got, err := d.feed([]byte("MD_END_pending" + home + clearScreen))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatal("Home/clear flushed pending marker text as terminal output")
			}
		}
	}
	for _, wire := range []string{"MD_BEGIN_pending\r\x1b[J", "MD_END_pending\r\x1b[0J", "\x1b[1;2H", "\x1b[2;1H", "\x1b[3J", "\x1b[2B"} {
		if _, err := newDecoder(512).feed([]byte(wire)); codeOf(err) != ProtocolUnknown {
			t.Fatal("clear/home exception expanded to unobserved movement or uncommitted contents", err)
		}
	}
}
