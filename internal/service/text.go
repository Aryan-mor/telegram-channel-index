package service

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/Aryan-mor/telegram-channel-index/internal/i18n"
	"github.com/Aryan-mor/telegram-channel-index/internal/store"
)

// pageSize is how many channels one result message holds.
const pageSize = 10

func refLabel(username, invite string, peer int64) string {
	if username != "" {
		return "@" + username
	}
	if peer != 0 {
		return fmt.Sprintf("c/%d", peer)
	}
	if invite != "" {
		return "+" + invite
	}
	return "c/0"
}

func hitLabel(h store.Hit) string { return refLabel(h.Username, h.InviteHash, h.PeerID) }

func oneLine(title string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range title {
		if unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
		if b.Len() >= 80 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func formatList(listID int64, tag string, page, pages int, hits []store.Hit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "list %d tag %s page %d/%d", listID, tag, page, pages)
	for i, h := range hits {
		n := h.N
		if n == 0 {
			n = i + 1
		}
		fmt.Fprintf(&b, "\n%d %s %+d %d", n, hitLabel(h), h.Score, h.Members)
		if title := oneLine(h.Title); title != "" {
			b.WriteByte(' ')
			b.WriteString(title)
		}
	}
	if page < pages {
		fmt.Fprintf(&b, "\nnext: /search %s %d", tag, page+1)
	}
	return b.String()
}

func voteLine(tag, label string, value, score int) string {
	v := strconv.Itoa(value)
	if value > 0 {
		v = "+" + v
	}
	return fmt.Sprintf("ok %s %s %s score %d", tag, label, v, score)
}

func callbackData(op string, listID int64, arg int) string {
	return fmt.Sprintf("v1:%s:%d:%d", op, listID, arg)
}

func keyboard(listID int64, n, page int, down bool, lang string) [][]Button {
	var rows [][]Button
	op := "u"
	if down {
		op = "d"
	}
	var row []Button
	if n > 10 {
		n = 10
	}
	for i := 1; i <= n; i++ {
		row = append(row, Button{Text: strconv.Itoa(i), Data: callbackData(op, listID, i)})
		if len(row) == 5 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	modeOp, modeLabel := "d", i18n.T(lang, "up")
	if down {
		modeOp, modeLabel = "u", i18n.T(lang, "down")
	}
	rows = append(rows, []Button{
		{Text: i18n.T(lang, "prev"), Data: callbackData("p", listID, page-1)},
		{Text: modeLabel, Data: callbackData(modeOp, listID, 0)},
		{Text: i18n.T(lang, "next"), Data: callbackData("p", listID, page+1)},
	})
	return rows
}
