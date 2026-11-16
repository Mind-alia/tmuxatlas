# Voice input from the browser

Programs on the server that capture from the default audio device, such as Claude Code `/voice`, need a microphone, and a remote server has none. With `TMUXATLAS_VOICE=on`, TmuxAtlas gives the server a virtual microphone fed by the browser.

```
browser tab mic → /ws/mic → pacat → the tab's PulseAudio null sink → monitor source → ALSA pulse plugin → SoX
```

## Setup

```bash
sudo apt-get install -y sox pulseaudio pulseaudio-utils libasound2-plugins
```

Set `TMUXATLAS_VOICE=on` for the TmuxAtlas service and restart it. At startup, and for each microphone stream, TmuxAtlas:

- starts PulseAudio for its user if needed
- creates the silent `atlas_mic` null sink and makes its monitor the default source, where recorders start
- creates one `atlas_mic_<id>` null sink per connected tab and removes it when the tab disconnects
- moves each new recorder to the right tab's microphone
- unloads `module-suspend-on-idle`, which would clip the start of dictation
- unloads `module-stream-restore`, which would start a recorder on another tab's microphone
- creates `~/.asoundrc` so ALSA clients use PulseAudio; an existing file that routes elsewhere is left alone and reported as an error in the Terminal toolbar

PulseAudio and `~/.asoundrc` are per user: a recorder only hears the audio server of the Unix user it runs as. Sessions served by an agent (`tmuxatlas agent`, another machine or another account on the same machine) therefore use **that agent's** virtual microphone:

```
browser tab mic → hub /ws/mic?host=<agent> → mic-open → agent dials /ws/peer-mic → the agent user's atlas_mic_<id> → SoX
```

Set `TMUXATLAS_VOICE=on` in the agent's service too, and restart it. The agent then performs the setup above for its own user and advertises the `voice` capability; **Talk** holds the space bar in its sessions through the PTY relay. Hub and agent must both run a build that has this relay.

## Usage

The microphone turns on when you open a local session; the browser asks for permission once. It belongs to that tab: another person connecting does not affect it. **● Mic on** turns it off, and this browser remembers that choice until you turn it back on. If the stream is interrupted, click **Mic** to restart it.

Claude Code needs voice enabled in hold mode, its default. Run `/voice hold` once if it is not; the setting persists.

1. Click **Talk** in the status bar below the Terminal. It is active only while Claude Code runs in the active pane. TmuxAtlas holds the space bar until you stop, so you can release the button while you speak.
2. When you finish, click one of the two buttons that replace **Talk**:
   - The **square** stops recording and leaves the transcription in the prompt, so you can edit it and press Enter.
   - The **arrow** stops recording and sends the prompt once Claude has inserted the transcription. If Claude was not listening or could not transcribe, nothing is sent.

## Limits

- **Hosts with voice only.** The button appears for sessions on the hub when it runs with `TMUXATLAS_VOICE=on`, and for sessions of an agent that runs with it. A session whose own user has no virtual microphone cannot dictate, even if the hub's microphone is on.
- **Secure context.** Browsers allow the microphone only over HTTPS or on `localhost`.
- **One microphone per tab, up to 16 per host.** Each tab has its own virtual microphone, so several people can dictate at the same time without hearing each other. A recorder hears the tab that last typed or pressed **Talk** in its tmux session; a recorder outside tmux, or in a session no tab has used, hears the newest tab. Two people in the same session share its recorder.
- **Claude prompt and default key binding.** **Talk** repeats the space bar on the server, because Claude treats a 200 ms gap as a release. It needs Claude's prompt and the default `space` binding; in a Claude dialog or the transcript viewer, the spaces act as key presses there. The arrow also reads Claude's footer on screen, so a Claude Code update that rewords it could stop the arrow from working.

Audio is forwarded to the host in real time while the microphone is on, whether or not you are dictating, and is never stored.

## Troubleshooting

| Symptom | Check |
|---|---|
| Dictation is silent while **Mic** is on | `pactl list short source-outputs` while recording: the recorder should be on an `atlas_mic_….monitor` source |
| `/voice` cannot open an audio device | As **the user running Claude Code**: `pactl info` shows `Default Source: atlas_mic.monitor`, and `~/.asoundrc` contains `pcm.!default { type pulse }`. `Connection refused` means that user has no PulseAudio: its TmuxAtlas (server or agent) does not run with `TMUXATLAS_VOICE=on` |
| No microphone button | `/api/voice?host=<host id>` returns `"enabled":true`: the hub has voice for its own sessions, or the session's agent runs with `TMUXATLAS_VOICE=on` and the same build |
| Recording is silent | `rec -q test.wav trim 0 3` while **Mic** is on, then `sox test.wav -n stat` |
| Wrong language | Change the dictation language in Claude Code with `/config` |
