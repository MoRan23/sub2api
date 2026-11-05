# Images GIF, SVG and complexity controls

Use `POST /v1/images/generations` with `output_format: "gif"` and an upstream
model that supports GIF output. Sub2API does not restrict GIF to GPT Image 2.6.
API Key requests use the mapped model; OAuth requests use the existing native
Images routes, including 1.5, 2, 2.5 and 2.6. The upstream determines actual GIF
availability. For example:

```json
{
  "model": "gpt-image-2.6-sunburst",
  "prompt": "A paper boat rocking gently on water, seamless loop",
  "output_format": "gif",
  "frame_count": 9,
  "frame_duration_ms": 200,
  "size": "auto"
}
```

Decode each `data[].b64_json` as base64 and save its complete bytes as a `.gif`
file. Sub2API preserves the animation, including its frames and timing. OAuth
requests using `response_format: "url"` receive a `data:image/gif;base64,...`
URL. Dimensions are read from the GIF canvas header without decoding all frames
or re-encoding the animation.

## Parameters and routing

- Optional `frame_count` must be an integer from 2 to 64.
- Optional `frame_duration_ms` must be an integer from 20 to 2000. The upstream
  rounds durations to 10 ms; Sub2API sends the requested value unchanged.
- Omitted animation controls stay omitted, allowing the upstream defaults.
- Frame controls require GIF output. GIF is generation-only and cannot be used
  with streaming, `partial_images`, `output_compression`, or `prompt_rewriter`.
- Account/channel model mapping runs before the final upstream route is chosen.
  Ordinary API Key requests retain their original fields; OAuth requests carry
  animation controls in the native Images payload. Provider-specific validation
  applies to the final GPT image model and OAuth adapter, preserving other
  providers' passthrough behavior.
- GIF/SVG requests never fall back from OAuth native Images to a Responses tool
  call. An unavailable native endpoint returns its upstream error, avoiding loss
  of output controls. Explicit `complexity` also requires the native Images route;
  other requests retain their existing fallback behavior.

Public API capabilities are documented by OpenAI. OAuth uses the separate Codex
Images endpoint, whose GIF availability depends on the upstream account and
service. This adaptation is verified with local animated fixtures and mock
upstreams; it does not send paid image requests.

## Response limits and billing

The existing `gateway.upstream_response_read_max_bytes` limit also applies to
GIF responses, including JSON and base64 overhead. The application default is
128 MiB; explicit deployment configuration takes precedence. A response exceeding
the configured limit returns the existing response-too-large error.

Usage-based billing continues to consume upstream usage. In image-count billing,
one returned animation counts as one output, not one output per frame. This
change does not add animation-specific pricing or alter configured prices.

## SVG output

Both 2.6 models accept `output_format: "svg"` on `/v1/images/generations`.
Omit `n` or use integer `1`. SVG does not support the edits endpoint, streaming,
`partial_images`, `output_compression`, `prompt_rewriter`, or GIF frame controls.
Validation happens after model mapping for GPT Images requests and before OAuth
request reconstruction; compatible providers keep their own validation rules.

The base64 payload contains UTF-8 XML. Save the decoded bytes as `.svg`.
Sub2API preserves those bytes and uses `data:image/svg+xml;base64,...` when
converting an OAuth response to `response_format: "url"`. It uses the actual
upstream output format when present, otherwise the requested format. It does not
rasterize the result or treat SVG viewBox coordinates as billable pixel sizes.

For API Key accounts with URL-to-base64 backfill enabled, an explicit SVG request
also permits downloading SVG output. Downloaded content must be a complete SVG
XML document; HTML, malformed XML and non-SVG roots are rejected. Existing URL,
proxy, redirect and response-size restrictions still apply. The gateway neither
renders the XML nor fetches referenced resources.

## Complexity and quality

`complexity` and `quality` are independent options. For example:

```json
{
  "model": "gpt-image-2.6-flare",
  "prompt": "An editable vector logo with a paper boat",
  "output_format": "svg",
  "complexity": "high",
  "quality": "max"
}
```

JSON and multipart requests retain `complexity` through OAuth native Images
reconstruction for generation and editing. API Key forwarding preserves its
original field. The gateway does not convert between quality and complexity or
insert either field when absent. OpenAI documents complexity values of `low`,
`medium`, and `high`; the upstream chooses its default for omitted complexity.
The gateway preserves the upstream response and parameter errors.

This support is verified using fixtures and simulated upstreams. Native OAuth
availability remains determined by the Codex Images service, including whether
GIF or SVG output is enabled for the account.

Reference: [OpenAI animated GIF output guide](https://developers.openai.com/api/docs/guides/image-generation#generate-animated-gif-output)
and [SVG output / GPT Image 2.6 controls](https://developers.openai.com/api/docs/guides/image-generation#generate-svg-output)
and [GPT Image 2.6 announcement](https://openai.com/index/introducing-images-2-6/).
