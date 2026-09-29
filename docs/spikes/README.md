# Spikes

Eight time-boxed experiments that gate phase 0 (shell) and phase 1 (agent loop). Run S1 and A1 first;
they hold the two biggest unknowns. Three more gate later work (R40–R54): A5 and S5 before phase 3's live
transcription and audio view; F1 before any framework pack beyond NeMo (deferred). Each brief ends with a Result section to fill
in.

| Id | Question | Box |
| --- | --- | --- |
| S1 | Can we drive Dockview floating groups per frame for snapping without breaking tab docking? | 2 days |
| S2 | Do shadcn/Base UI popovers, tooltips and the command palette work inside a Dockview popout window? | 1 day |
| S3 | Does a Slate + Indigo theme cover every Dockview surface, light and dark? | 1 day |
| S4 | Does a 20-panel workspace restore under 300 ms? | 1 day |
| A1 | Does one ACP client drive both `opencode acp` and `claude-agent-acp` for chat, tool-call diffs, permissions, cancel, resume? | 2 days |
| A2 | Can both agents create a mix and dry-run a training job through the Cadence MCP server? | 2 days |
| A3 | Does a Nemotron 3.5 fine-tune run on the staging card under a 24 GB cap, export to ONNX and pass parity? | 2 days |
| A4 | Do agent edits reach an open panel live through outbox → SSE → cache patching? | 1 day |
| A5 | Does live microphone audio reach a Nemotron checkpoint through the WebSocket relay and come back as words within the latency budget, beside a training job? | 2 days |
| S5 | Does one audio view render a 30-minute call with spectrogram and word tracks at 60 fps, across popouts, within WebGL limits? | 2 days |
| F1 | Does a second training framework (k2/icefall) pass the conformance suite without changes outside its pack? | 3 days |

Status legend: `todo` · `running` · `done` · `partial` · `failed` · `deferred`.
