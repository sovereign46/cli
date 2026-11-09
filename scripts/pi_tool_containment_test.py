import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import runpy
import unittest


runner = runpy.run_path(Path(__file__).with_name("pi-tool-containment.py"))
CHALLENGE = "a" * 64
ARTIFACT = "b" * 64
MODEL = "test-backend"
REQUEST = json.dumps({
    "model": MODEL, "messages": [{"role": "system", "content": "fetch an inert package\nexact command"}],
}).encode()
NEXT_REQUEST = REQUEST.replace(b'"messages": [', b'"messages": [{"role":"tool","content":"tool result"}, ')
STOP = b'data: {"choices":[{"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
TOOL = b'data: {"choices":[{"finish_reason":"tool_calls"}]}\n\ndata: [DONE]\n\n'


def witness(sequence, request, response):
    return {
        "schema": "s46.pi-bridge-witness.v1", "challenge": CHALLENGE,
        "releaseId": "sha256:" + "c" * 64, "planSha256": "sha256:" + "d" * 64,
        "inputLockSha256": "sha256:" + "e" * 64, "artifactSha256": "sha256:" + ARTIFACT,
        "runtimeProfileSha256": "sha256:" + "1" * 64, "runtimeClosureSha256": "sha256:" + "2" * 64,
        "runId": "fixture-run", "modelId": "s46/fixture", "modelVersion": "v1", "backendModel": MODEL,
        "sequence": sequence, "requestSha256": hashlib.sha256(request).hexdigest(),
        "responseSha256": hashlib.sha256(response).hexdigest(),
        "startedAt": "2026-11-09T01:00:00.123456789Z", "finishedAt": "2026-11-09T01:00:01Z",
    }


class QualificationWitnessTest(unittest.TestCase):
    def bridge(self, response, *, change=None, header_count=1, challenge=CHALLENGE):
        observed = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                request = self.rfile.read(int(self.headers["Content-Length"]))
                observed.append((self.headers.get(runner["CHALLENGE_HEADER"]), request))
                data = response(len(observed))
                record = witness(len(observed), request, data)
                if change:
                    record.update(change(len(observed)))
                encoded = base64.urlsafe_b64encode(json.dumps(record, separators=(",", ":")).encode()).decode().rstrip("=")
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Content-Length", str(len(data)))
                for _ in range(header_count):
                    self.send_header(runner["WITNESS_HEADER"], encoded)
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        server = self.enterContext(runner["serve"](ThreadingHTTPServer(("127.0.0.1", 0), Handler)))
        provider = runner["Provider"](f"http://127.0.0.1:{server.server_port}/v1/chat/completions", challenge, ARTIFACT)
        provider.start({"caseId": "agents-md", "command": "exact command", "token": "fixture-token"}, MODEL)
        return provider, observed

    def test_captures_consecutive_exact_exchanges(self):
        provider, observed = self.bridge(lambda sequence: TOOL if sequence == 1 else STOP)
        self.assertEqual(provider.exchange(REQUEST), TOOL)
        self.assertFalse(provider.receipt()["finished"])
        self.assertEqual(provider.exchange(NEXT_REQUEST), STOP)
        result = provider.receipt()
        self.assertTrue(result["finished"])
        self.assertTrue(result["contextObserved"])
        self.assertEqual(len(result["bridgeWitnesses"]), 2)
        for index, ((challenge, body), encoded) in enumerate(zip(observed, result["bridgeWitnesses"]), start=1):
            self.assertEqual(challenge, CHALLENGE)
            self.assertEqual(json.loads(base64.urlsafe_b64decode(encoded + "=" * (-len(encoded) % 4))),
                             witness(index, body, TOOL if index == 1 else STOP))

    def test_missing_duplicate_stale_and_misbound_witnesses_cannot_retry(self):
        for header_count, change in ((0, {}), (2, {}), (1, {"challenge": "f" * 64}),
                                     (1, {"sequence": 2}), (1, {"requestSha256": "f" * 64}),
                                     (1, {"responseSha256": "f" * 64}), (1, {"artifactSha256": "sha256:" + "f" * 64})):
            with self.subTest(header_count=header_count, change=change):
                provider, observed = self.bridge(lambda _: STOP, change=lambda _: change, header_count=header_count)
                with self.assertRaises(ValueError):
                    provider.exchange(REQUEST)
                with self.assertRaisesRegex(ValueError, "unexpected qualification model request"):
                    provider.exchange(REQUEST)
                self.assertFalse(provider.receipt()["finished"])
                self.assertEqual(provider.receipt()["bridgeWitnesses"], [])
                self.assertEqual(len(observed), 1)

    def test_requires_exact_fixture_context_and_stable_run_identity(self):
        provider, observed = self.bridge(lambda _: TOOL, change=lambda turn: {"runId": str(turn)})
        self.assertEqual(provider.exchange(REQUEST), TOOL)
        with self.assertRaisesRegex(ValueError, "identity changed"):
            provider.exchange(NEXT_REQUEST)
        self.assertFalse(provider.receipt()["finished"])
        self.assertEqual(len(observed), 2)

        provider, observed = self.bridge(lambda _: STOP)
        with self.assertRaisesRegex(ValueError, "unexpected qualification model request"):
            provider.exchange(REQUEST.replace(b'"system"', b'"user"'))
        self.assertEqual(observed, [])

    def test_does_not_forward_a_retried_turn(self):
        provider, observed = self.bridge(lambda _: TOOL)
        self.assertEqual(provider.exchange(REQUEST), TOOL)
        with self.assertRaisesRegex(ValueError, "duplicate qualification model request"):
            provider.exchange(REQUEST)
        self.assertFalse(provider.receipt()["finished"])
        self.assertEqual(len(observed), 1)

    def test_standalone_live_observation_requires_no_witness(self):
        provider, observed = self.bridge(lambda _: STOP, header_count=0, challenge=None)
        self.assertEqual(provider.exchange(REQUEST), STOP)
        self.assertIsNone(observed[0][0])
        self.assertTrue(provider.receipt()["finished"])
        self.assertNotIn("bridgeWitnesses", provider.receipt())


if __name__ == "__main__":
    unittest.main()
