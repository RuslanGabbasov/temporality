"""Dependency-free HTTP client for the universal Temporality API."""

from __future__ import annotations

import json
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


class TemporalityClient:
    def __init__(self, base_url: str = "http://localhost:8080", timeout: float = 5.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def _post(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        request = Request(
            self.base_url + path,
            data=json.dumps(body).encode("utf-8"),
            headers={"Content-Type": "application/json", "Accept": "application/json"},
            method="POST",
        )
        try:
            with urlopen(request, timeout=self.timeout) as response:
                return json.loads(response.read())
        except HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace")
            raise RuntimeError(f"Temporality returned HTTP {error.code}: {detail}") from error
        except URLError as error:
            raise RuntimeError(f"Temporality is unavailable: {error.reason}") from error

    def record(self, *events: dict[str, Any]) -> dict[str, Any]:
        return self._post("/v1/observations/events", {"events": list(events)})

    def hints_after_tool(
        self,
        *,
        project: str,
        task: str,
        tool: str,
        tool_result: str,
        run: str = "",
        actor: dict[str, str] | None = None,
        entities: list[str] | None = None,
        topics: list[str] | None = None,
        limit: int = 8,
    ) -> dict[str, Any]:
        """Return supplemental memory context; tool_result remains untouched."""
        body: dict[str, Any] = {
            "project": project,
            "task": task,
            "tool": tool,
            "tool_result": tool_result,
            "limit": limit,
        }
        if run:
            body["run"] = run
        if actor:
            body["actor"] = actor
        if entities:
            body["entities"] = entities
        if topics:
            body["topics"] = topics
        return self._post("/v1/observations/hints", body)
