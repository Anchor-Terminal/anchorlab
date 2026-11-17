# Running the models

How the lab runs its models: llama.cpp on Debian 13, on an AMD Ryzen AI Max+ 395 (Strix Halo) with
128 GB of unified memory, through Vulkan. One `llama-server` in router mode serves all of them on
`127.0.0.1:8080`, where the harness expects it, and loads whichever one a request names. So
`anchor-lab bench -model gpt-oss-120b` and then `-model gemma-4-31b` just work, one after the
other, with nothing restarted.

The models are the ones [Anchor Terminal's calculator](https://www.anchorterminal.com/open-weights/calculator/)
picks for that machine with llama.cpp, for agents and tool calling.

| Name (what the harness asks for) | Model | Build | File | Calculator's estimate |
| --- | --- | --- | --- | --- |
| `qwen3.8-27b` | [Qwen3.8-27B](https://www.anchorterminal.com/tools/qwen3-8-27b) | Q4_K_M, ggml-org | 19.4 GB | the calculator's pick |
| `qwen3.6-27b` | [Qwen3.6-27B](https://www.anchorterminal.com/tools/qwen3-6-27b) | Q4_K_M, ggml-org | 19.4 GB | ~9.2 tokens/s |
| `qwen3.5-122b-a10b` | [Qwen3.5-122B-A10B](https://www.anchorterminal.com/tools/qwen3-5-122b-a10b) | Q4_K_M, LM Studio community, 2 files | 74.2 GB | ~27.0 tokens/s |
| `gemma-4-31b` | [Gemma 4 31B IT](https://www.anchorterminal.com/tools/gemma-4-31b-it) | Q4_0 QAT, Google | 17.7 GB | ~8.3 tokens/s |
| `gemma-4-26b-a4b` | [Gemma 4 26B A4B IT](https://www.anchorterminal.com/tools/gemma-4-26b-a4b-it) | Q4_0 QAT, Google | 14.4 GB | ~58.1 tokens/s |
| `glm-4.7-flash` | [GLM-4.7-Flash](https://www.anchorterminal.com/tools/glm-4-7-flash) | Q8_0, ggml-org | 31.8 GB | ~52.3 tokens/s |
| `qwen3.6-35b-a3b` | [Qwen3.6-35B-A3B](https://www.anchorterminal.com/tools/qwen3-6-35b-a3b) | Q8_0, ggml-org | 36.9 GB | ~51.7 tokens/s |
| `gpt-oss-120b` | [gpt-oss-120b](https://www.anchorterminal.com/tools/gpt-oss-120b) | MXFP4, ggml-org | 63.4 GB | ~58.3 tokens/s |

About 277 GB together. The estimates are the calculator's, for one stream, from the machine's
memory bandwidth (256 GB/s) and the gigabytes read for each token. They aren't measurements; the
bench and the lab's own runs are. The calculator lists Q4_K_M for both Gemma models; Google
publishes quantisation-aware trained Q4_0 builds, made to be run at 4 bits, and those are what's
here. The calculator's GB figures include room for the runtime and a context, so they're larger
than the files.

Exact files, sizes and SHA-256s are in `models/models.txt`.

## 1. The GPU

The box needs the GPU to have most of the memory, and a recent Mesa for its Vulkan driver (RADV).

- **Kernel parameters**, in `GRUB_CMDLINE_LINUX_DEFAULT` in `/etc/default/grub`, then
  `update-grub` and a reboot. This gives the GPU about 100 GB of GTT:

  ```
  amdgpu.gttsize=102400 ttm.pages_limit=26214400 ttm.page_pool_size=26214400
  ```

- **Firmware and a kernel that knows the chip.** Debian 13's backports kernel and
  `firmware-amd-graphics`:

  ```
  apt install -t trixie-backports linux-image-amd64 firmware-amd-graphics
  ```

- **Mesa from backports**, which is 26.1 where trixie has 25.0:

  ```
  apt install -t trixie-backports mesa-vulkan-drivers vulkan-tools
  vulkaninfo --summary        # a device named AMD Radeon ... (RADV ...)
  ```

## 2. llama.cpp

Debian packages llama.cpp only in testing and unstable, and router mode, the presets and the newest
models all need a recent build, so it's built from source with Vulkan:

```
apt install build-essential cmake git libssl-dev libvulkan-dev glslc spirv-headers
git clone --depth 1 https://github.com/ggml-org/llama.cpp
cd llama.cpp
cmake -B build -DGGML_VULKAN=ON -DCMAKE_BUILD_TYPE=Release
cmake --build build -j"$(nproc)"
cmake --install build --prefix /usr/local
ldconfig
llama-server --list-devices    # Vulkan0: AMD Radeon ... with about 100 GB
```

To update later: `git pull`, the two `cmake --build` and `--install` lines again, and
`systemctl restart llama-server`.

## 3. The models

The models folder is `/srv/models` unless `MODELS` says otherwise, laid out by maker and model
(`/srv/models/openai/gpt-oss-120b/`). It wants its own volume: these files are big, and a full
`/var` takes a machine down.

```
models/fetch.sh list            # the list, and which are here
models/fetch.sh gpt-oss-120b    # one model
models/fetch.sh all             # all eight, about 277 GB
```

Each file is checked against its SHA-256 from `models/models.txt` once it's down. A download that
stops picks up where it stopped. A file that fails the check isn't used, and the script says which.
Run it as a user that can write the models folder; nothing in it needs root.

## 4. The server

```
MODELS=/srv/models bash models/install.sh
```

It makes the user `llama` (no shell, in the `render` and `video` groups for the GPU), puts
`models/presets.ini` in `/etc/llama/models.ini`, and starts `llama-server.service`: router mode,
`127.0.0.1:8080`, one model loaded at a time (`--models-max 1`), the models folder read-only, and no
network but this machine. It never downloads anything; `fetch.sh` does that first.

```
curl -s http://127.0.0.1:8080/models | head -c 600     # the eight names
curl -s http://127.0.0.1:8080/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-26b-a4b","messages":[{"role":"user","content":"Say hello in five words."}]}'
journalctl -u llama-server -f
```

The first request for a model loads it, which takes from a few seconds to a minute or so for the
63 GB of gpt-oss-120b. Asking for a different model unloads the one before.

## 5. The settings, model by model

`models/presets.ini` holds them. Everything shares one slot (the lab asks one thing at a time), all
layers on the GPU, a 65,536-token context, and a 2,048-token cap on thinking. The cap is there
because thinking counts against the harness's 4,096-token reply: without it, a model that thinks
at length runs out before it writes the program. Raise both together, `-max-tokens` on the harness
and `reasoning-budget` here.

Sampling is each publisher's own recommendation, from their model card. The harness sends no
temperature of its own unless `ANCHOR_LAB_TEMPERATURE` is set, so these apply.

| Name | temperature | top_p | top_k | min_p | presence_penalty | Thinking |
| --- | --- | --- | --- | --- | --- | --- |
| `qwen3.8-27b` | 1.0 | 0.95 | 20 | 0 | 0 | on; `reasoning-effort = medium` (its default, xhigh, is too long for a lab reply) |
| `qwen3.6-27b` | 1.0 | 0.95 | 20 | 0 | 0 | on by default |
| `qwen3.5-122b-a10b` | 1.0 | 0.95 | 20 | 0 | 1.5 | on by default |
| `gemma-4-31b` | 1.0 | 0.95 | 64 | 0 | | off unless asked for |
| `gemma-4-26b-a4b` | 1.0 | 0.95 | 64 | 0 | | off unless asked for |
| `glm-4.7-flash` | 1.0 | 0.95 | | 0 | | on by default |
| `qwen3.6-35b-a3b` | 1.0 | 0.95 | 20 | 0 | 1.5 | on by default |
| `gpt-oss-120b` | 1.0 | 1.0 | | 0 | | `reasoning-effort = medium` (low, medium or high) |

Notes:

- **Qwen.** The 27B models are dense, so every token reads all 19 GB: slow on this machine, under
  10 tokens a second. The two MoE models read only their active experts (3B or 10B a token) and
  are several times faster. The publisher's non-thinking settings are temperature 0.7, top_p 0.8
  and presence_penalty 1.5, for a preset of your own that turns thinking off.
- **Qwen3.5-122B-A10B** comes in two files; the preset names the first and llama.cpp finds the
  second beside it.
- **Gemma 4** thinks only when the request turns it on. A preset of your own with
  `chat-template-kwargs = {"enable_thinking":true}` does that for every request.
- **GLM-4.7-Flash** has a 202,752-token context. Z.ai ran its terminal and SWE benchmarks at
  temperature 0.7 and top_p 1.0.
- **gpt-oss-120b** has a 131,072-token context and is the fastest of the big ones here, since only
  about 5B of its 117B parameters are active a token.

## 6. Picking one

```
for m in qwen3.8-27b qwen3.6-27b qwen3.5-122b-a10b gemma-4-31b gemma-4-26b-a4b glm-4.7-flash qwen3.6-35b-a3b gpt-oss-120b; do
    sudo -u anchorlab anchor-lab bench -model "$m"
done
sudo -u anchorlab anchor-lab bench -compare
```

The winner's name goes in `/etc/anchor-lab/env` as `ANCHOR_LAB_MODEL`, then
`systemctl restart anchor-lab`. Bench them with the private set (`build.sh <file>`, README.md):
the public sample is too small to pick a model on.

## 7. Faster, optionally

Several of the ggml-org repositories also have small draft models for speculative decoding: `mtp-`
files for multi-token prediction (Qwen, Gemma), `dflash-` files (Qwen, Gemma), and an `eagle3-`
file for gpt-oss-120b. llama.cpp takes them with `spec-type` (`draft-mtp`, `draft-dflash`,
`draft-eagle3`) and `model-draft` in a preset. We haven't measured them on this machine yet, and
they aren't in `models.txt`: try one model at a time, and keep it only if the bench says it's
faster with the same score.

## When it doesn't work

- **`vulkaninfo` shows llvmpipe and no Radeon.** The firmware or Mesa is missing or old: section 1.
- **Out of device memory on load.** The GTT parameters didn't take (`cat /proc/cmdline`), or
  another model is still loaded. `curl -s http://127.0.0.1:8080/models` says what's loaded.
- **The harness says the model isn't answering.** `ANCHOR_LAB_MODEL_URL` must be
  `http://127.0.0.1:8080/v1`. llama-server's own default port is 9931, so the unit sets 8080.
- **The model's name isn't known.** The name in `ANCHOR_LAB_MODEL` must be a section of
  `/etc/llama/models.ini`, spelled the same.
- **Replies come back empty or cut off.** The model spent the reply thinking: lower its
  `reasoning-budget` or `reasoning-effort`, or raise the harness's `-max-tokens`.
