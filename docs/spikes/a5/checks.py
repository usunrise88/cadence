"""A5: the relay's gate checks without a browser: a foreign Origin, a missing Origin, a wrong ticket, an expired one
is not waited for (60 s; the code path is the same as "wrong"). Each must be refused with 403 before any upgrade.
   docs/spikes/a5/run.sh python /a5/checks.py
"""

import asyncio
import json

from aiohttp import ClientSession, WSServerHandshakeError

R = "http://127.0.0.1:18480"


async def attempt(http: ClientSession, url: str, origin: str | None) -> str:
    try:
        async with http.ws_connect(R.replace("http", "ws") + url, origin=origin) as ws:
            await ws.close()
            return "opened"
    except WSServerHandshakeError as e:
        return f"refused {e.status}"


async def main() -> None:
    out = {}
    async with ClientSession() as http:
        for name, origin, mangle in (
            ("foreign origin", "https://evil.example", False),
            ("no origin", None, False),
            ("wrong ticket", R, True),
        ):
            async with http.post(f"{R}/api/transcriptions", json={}) as r:
                s = await r.json()
            url = s["streamUrl"] + ("x" if mangle else "")
            out[name] = await attempt(http, url, origin)
    print(json.dumps(out))


asyncio.run(main())
