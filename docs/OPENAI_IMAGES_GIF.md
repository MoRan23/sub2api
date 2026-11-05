# GPT Image 2.6 GIF output

Use `POST /v1/images/generations` with either `gpt-image-2.6-sunburst`
or `gpt-image-2.6-flare` and `output_format: "gif"`:

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
- GIF requests never fall back from OAuth native Images to a Responses tool
  call. An unavailable native endpoint returns its upstream error, avoiding loss
  of animation controls. Existing non-GIF fallback behavior is unchanged.

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

Reference: [OpenAI animated GIF output guide](https://developers.openai.com/api/docs/guides/image-generation#generate-animated-gif-output)
and [GPT Image 2.6 announcement](https://openai.com/index/introducing-images-2-6/).
