# Voice input from the browser

Programs on the server that capture from the default audio device, such as Claude Code and agy `/voice`, need a microphone, and a remote server has none. With `TMUXATLAS_VOICE=on`, TmuxAtlas gives the server a virtual microphone fed by the browser.

```
browser tab mic → /ws/mic → pacat → the tab's PulseAudio null sink → monitor source → ALSA pulse plugin → SoX
```

## Setup

```bash
sudo apt-get install -y sox pulseaudio pulseaudio-utils libasound2-plugins alsa-utils
```

agy records with `arecord`, from `alsa-utils`; without it, its `/voice` reports "No microphone recorder found".

Set `TMUXATLAS_VOICE=on` for the TmuxAtlas service and restart it. At startup, and for each microphone stream, TmuxAtlas:

- starts PulseAudio for its user if needed
- creates the silent `atlas_mic` null sink and makes its monitor the default source, where recorders start
- creates one `atlas_mic_<id>` null sink per connected tab and removes it when the tab disconnects
- moves each new recorder to the right tab's microphone
- unloads `module-suspend-on-idle`, which would clip the start of dictation
- unloads `module-stream-restore`, which would start a recorder on another tab's microphone
- creates `~/.asoundrc` so ALSA clients use PulseAudio; an existing file that routes elsewhere is left alone and reported as an error beside the header's microphone indicator

Run the agent as the same user as TmuxAtlas, since PulseAudio and `~/.asoundrc` are per user.

## Usage

The microphone turns on when Atlas connects to its local host; the browser asks for permission once. It belongs to that tab: another person connecting does not affect it. **Micro actif** in the app header shows that it is running. It stays active as you navigate between views, sharing one stream with the Terminal and every Wall tile. If the stream is interrupted, click **Réessayer le micro** in the header to restart it.

Claude Code needs voice enabled in hold mode, its default. Run `/voice hold` once if it is not; the setting persists.

1. Click **Talk** in the app header above the Terminal, or in a tile header on the Wall. It is active only while Claude Code or agy runs in the active pane. For Claude, TmuxAtlas holds the space bar until you stop, so you can release the button while you speak. For agy, it presses `F5`, which starts agy's dictation, and `F5` again when you stop.
2. When you finish, click one of the two buttons that replace **Talk**:
   - The **square** stops recording and leaves the transcription in the prompt, so you can edit it and press Enter.
   - The **arrow** stops recording and sends the prompt once the agent has inserted the transcription. If it was not listening or could not transcribe, nothing is sent.

agy is found through its process tree, so Talk also works when a launcher starts it and the pane shows another command, such as `python3`. agy allows one dictation stream at a time per account.

## Codex: dictation through a hidden Claude

Codex has no dictation into its prompt, so **Talk** borrows Claude Code's. When Codex runs in the active pane:

1. TmuxAtlas starts, once, a hidden tmux session named `tmuxatlas-dictation-…` running `claude` in its own empty folder (`~/.local/share/tmuxatlas/dictation`). It answers the account menu of a launcher such as ai-accounts with its default, and Claude's folder trust dialog with yes. With `TMUXATLAS_DICTATION_PERSON` set, TmuxAtlas starts that hidden Claude as soon as it starts itself, and keeps it running, even across its own restarts, so dictations start at once. Otherwise the first dictation of a person waits a few seconds while Claude starts.
2. The tab's microphone is routed to that session, and TmuxAtlas holds its space bar while you speak. The transcript Claude writes as you speak shows live next to **Talk**.
3. When you stop, it reads the transcript from the hidden prompt, clears that prompt, and types the text in Codex. The **square** leaves it to edit; the **arrow** also presses Enter.

Nothing is ever submitted in the hidden Claude, so it runs no turn. Its account is the session's person (its `@ai-person` tmux option), or always `TMUXATLAS_DICTATION_PERSON` when set, for example `Aris`. That account needs Claude's voice in hold mode (`/voice hold`). One dictation runs at a time per account. Another person's hidden session stops after 30 minutes without dictation, and at the next start of TmuxAtlas. Hidden sessions never appear in TmuxAtlas.

## Limits

- **Local sessions only.** The button appears only for sessions on the machine serving the page. Sessions on other hosts are not relayed.
- **Secure context.** Browsers allow the microphone only over HTTPS or on `localhost`.
- **One microphone per tab, up to 16 per host.** Each tab has its own virtual microphone, so several people can dictate at the same time without hearing each other. A recorder hears the tab that last typed or pressed **Talk** in its tmux session; a recorder outside tmux, or in a session no tab has used, hears the newest tab. Two people in the same session share its recorder.
- **agy's footer.** TmuxAtlas presses `F5` only when agy's footer shows its prompt or a recording, and the arrow sends only once the transcription is in agy's prompt. An agy update that rewords `Recording` or `Finishing up...` could stop Talk from working with it.
- **Claude prompt and default key binding.** **Talk** repeats the space bar on the server, because Claude treats a 200 ms gap as a release. It needs Claude's prompt and the default `space` binding; in a Claude dialog or the transcript viewer, the spaces act as key presses there. The arrow also reads Claude's footer on screen, so a Claude Code update that rewords it could stop the arrow from working.

Audio is forwarded to the host in real time while the microphone is on, whether or not you are dictating, and is never stored.

## Troubleshooting

| Symptom | Check |
|---|---|
| Dictation is silent while **Mic** is on | `pactl list short source-outputs` while recording: the recorder should be on an `atlas_mic_….monitor` source |
| `/voice` cannot open an audio device | `pactl info` shows `Default Source: atlas_mic.monitor`, and `~/.asoundrc` contains `pcm.!default { type pulse }` |
| No microphone indicator | `curl localhost:7654/api/voice` returns `"enabled":true`, and Atlas has connected to its local host |
| Recording is silent | `rec -q test.wav trim 0 3` while **Mic** is on, then `sox test.wav -n stat` |
| Wrong language | Change the dictation language in Claude Code with `/config` |
