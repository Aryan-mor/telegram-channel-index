# Plan: `telegram-channel-index` (new public Go repo)

## Context
This is a new bot, modelled on `telegram-hashtag-index`. That bot indexes posts; this one indexes whole channels under fixed tags.

- **Who submits:** people and bots.
- **Searching:** a tag returns 10 channels per page, with Prev/Next and buttons 1..10.
- **Voting:** pressing a number means "this one was good". A bot has no buttons, so the same vote exists as `/accept` or `/reject` in code. A bot's vote weighs 1, the same as a person's.
- **Score:** `/reject` is a downvote. Each voter has one vote per (channel, tag): +1, -1 or none, and a new vote replaces the old one. Results are sorted by score.
- **Replies stay human.** Usage text, errors, `/start`, and vote replies never say that a bot can call this bot, and never show the machine forms below. This plan is the only write-up. A caller learns the forms by reading the code.
- **Later use:** jellyfin-telegram will use it to discover "news" sources (channels that post file-bot links) and "direct" sources (channels that post video files).

This file moves to the new repo once it exists.

## Start from telegram-hashtag-index (sibling folder)
These parts are copied almost unchanged:
- `cmd/hashtag-index/main.go`: `serve`/`login`/`say` and the verifier reconnect loop.
- `internal/userauth` and `internal/service/limiter.go`.
- The `store.Open` pattern: one schema const with `CREATE IF NOT EXISTS`, WAL, one connection.
- The silence rules:
  - never answer plain text;
  - no reply starts with `/` or `#`;
  - strangers get nothing when `OPEN=false`.
- Dockerfile and `docker-compose.yml` (read-only, 128m).
- `.github/workflows/ci.yml`: vet, `test -race`, push to ghcr, optional deploy through the tunnel.

**`internal/botapi` needs these changes.** Today it only handles plain-text `message` updates.
- Add `MessageID`, `ReplyTo`, and `From.Username`/`LanguageCode` to the message type.
- Add a `CallbackQuery` type.
- Set `allowed_updates` to `["message","callback_query"]`.
- `Send` returns the message id.
- New calls: `SendMarkup`, `EditText`, `AnswerCallback`.

**`service.Handle` needs this change.** It returns `[]Action` (send, edit or answer) instead of one string, so it stays a pure function that is easy to test.

## Layout
```
cmd/channel-index/main.go   serve | login | say
internal/botapi/            + keyboards, callbacks, edit, answerCallbackQuery
internal/tags/              fixed ASCII slugs: movie, series, anime, documentary, music, joke,
                            meme, news, direct-file, software, education, book
internal/i18n/              labels en (default) + fa; machine lines never translated
internal/store/             schema + queries
internal/service/           Handle(update) -> []Action, conv.go, limiter.go
internal/verify/            user-account worker
internal/userauth/          copied
```
Tags are fixed in code, so adding a tag needs a new release. Users type only the slug.

## Data model
```sql
channels(id PK, peer_id UNIQUE, username UNIQUE NOCASE, invite_hash, title, members,
  last_post_unix, state pending|live|dead|hidden, submitted_by, created_unix, checked_unix, tries)
channel_tags(channel_id, tag, score, up, down, added_by, created_unix, PK(channel_id, tag))
  INDEX (tag, score DESC, channel_id)
votes(voter_id, channel_id, tag, value -1|1, weight, voter_is_bot, at_unix, PK(voter_id, channel_id, tag))
lists(id PK, chat_id, message_id, owner_id, tag, page, created_unix, UNIQUE(chat_id, message_id))
list_items(list_id, n 1..10, channel_id, PK(list_id, n))
members(...)   -- unchanged roles/ban bits
```
- **A vote** runs in one transaction: read the old vote, upsert or delete the row, then apply `delta = new*w - old*w` to `score`, `up` and `down`.
- **Sort order** is `score DESC, members DESC, channel_id`. Paging is by page number, 10 per page. A tag will hold hundreds of channels, not millions.
- **Snapshots:** `list_items` saves each page as shown. That way option N is always the channel the voter actually saw.

## Text protocol
Each reply fits in one message, because `TalkToBot` in jellyfin-telegram reads only the first reply.
```
list 4812 tag news page 2/7
1 @somechannel +14 18300 Title text
2 c/1029384756 +9 2100 Private title
next: /search news 3
```

**Commands:**
- `/tags`
- `/search <tag> [page]`
- `/add @chan tag1 [tag2..]`. It also takes a `t.me/+hash` link when `PRIVATE_LINKS=true`.
- `/tag @chan <tag>`
- `/info @chan`

**`/accept` and `/reject` take three forms:**
1. `/accept 3` or `/accept option3`, sent as a **reply** to a list message. The bot looks up `lists` by `(chat_id, reply_to.message_id)`.
2. `/accept 4812:3`, with no reply. This form is for TalkToBot and for bots.
3. `/accept @chan news`.

`/unvote` takes the same forms. Sending the same `/accept` twice changes nothing, so a repeat gets the same one-line result. Each vote gets a one-line reply such as `ok news @chan +1 score 15`.

That line is the whole reply. It does not add a usage example, a list of forms, or any hint that another bot can send these commands. Unknown input gets the same silence as plain text, not a help message. `/tags` output is the tag list only.

**Admin commands:** `/hide`, `/untag`, `/deny`, `/allow`, and `/purge <voter>`, which deletes that voter's votes and recomputes the scores.

## Buttons
- `callback_data` format is `v1:<op>:<listId>:<arg>`, where op is `p` (page), `u` (up) or `d` (down).
  - The longest value is 18 bytes, well under Telegram's 64-byte limit. A test checks this.
- **Keyboard:**
  - rows `1..5` and `6..10`;
  - then `Prev | Up/Down mode | Next`;
  - in Down mode the number buttons send downvotes.
- **Prev/Next** edits the same message and replaces that list's items.
- Pressing the same vote again clears it.
- Every callback gets an `answerCallbackQuery` with a short message.

## Channel verification
A user account checks each submitted channel. It does 20 checks per minute, waits out FLOOD_WAIT, and marks a channel dead after 10 failed tries.
- **Public channels:**
  - `ContactsResolveUsername`;
  - `ChannelsGetFullChannel` for the member count;
  - `MessagesGetHistory` limit 1 for the last post.
- **Rejected:** megagroups, and channels with no post in 90 days.
- **Private links** are checked with `MessagesCheckChatInvite`, which never joins.
- Channels still `pending` are hidden from search.
- The verifier has its own session file, separate from jellyfin-telegram's.

## Abuse controls
- **One vote per account** per (channel, tag), enforced by the primary key.
- **Rate limits:**
  - `v:` 30 votes per hour;
  - `s:` 10 submits per day;
  - `r:` 60 searches per hour.
- **Vote weights:** a person's vote counts 1, a bot's vote counts 1, and a trusted person's counts 2. No admin step for bots. If throwaway bots become a problem later, handle it then.
- `/purge` cleans up whatever gets past these.

## Phases
- **P0:**
  - Copy the skeleton and rename it.
  - Extend `botapi` and test it with an httptest fake.
  - Spike: in a test group, check that a bot with "Bot-to-bot Communication Mode" on receives `reply_to_message` when it replies to another bot.
- **P1:** `/add`, `/tag`, tags, the verify worker, `/info`.
- **P2:** `/search` text format, list snapshots, keyboards, Prev/Next edits.
- **P3:** button votes, the three forms of `/accept` and `/reject`, `/unvote`, weights, `/purge`.
- **P4:**
  - Push to ghcr.
  - Deploy to Hetzner in `/srv/channel-index`.
  - Never build the image on that box.
- **P5 (in jellyfin-telegram):**
  - New `internal/chanindex/client.go`, modelled on `internal/hashindex/client.go` and `tguser.TalkToBot`:
    - `Search(tag, page)` parses the item lines;
    - `Vote(ref, tag, ±1)` uses form 3.
  - New setting `CHANNEL_INDEX_BOT`.
  - A "Discover channels" box in the News and Direct tabs:
    - tag `news` feeds `news_add`;
    - tag `direct-file` feeds `add`;
    - both go through `sourceActions` (`cmd/jellyfin-telegram/sources.go`).
  - **Auto-accept:** once a channel added from Discover has brought in at least 3 files, send `/accept` once. A `chanindex_voted` table prevents repeats.

## Risks
- Privacy mode may hide replies between bots in groups. The P0 spike checks this.
- The verify account can get flood limits.
- People or bots can game the votes. Each account, bot or person, has one vote of weight 1 (a trusted person has 2). `/purge` is there if that becomes a problem. A Wilson-style ranking can come later.

## Verification
- **Store:** step through the transition table none→+1→-1→none, and check `score == SUM(value*weight)` after each step.
- **Service:**
  - every `/accept` form;
  - the `option3` alias;
  - `@botname` stripping;
  - silence rules, including no usage text on an unknown command;
  - an unknown list id;
  - a vote reply is only the one-line result, with no command template in it.
- **botapi:** an httptest fake records each edit and answer call.
- **Live:** in a test chat with two people and one bot:
  - add a channel, verify it, search, page;
  - vote up, vote down, replace a vote;
  - a bot sends `/accept` both by reply and by `listId:n`.
