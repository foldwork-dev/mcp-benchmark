# mcp-benchmark

Before installing anything, run this on your own codebase to see exactly how much you'd save with mcp-injector.

```bash
curl -fsSL https://raw.githubusercontent.com/foldwork-dev/mcp-benchmark/main/install.sh | sh
mcp-benchmark ./your-project
```

Takes about 5 seconds. Shows you token counts, compression ratios, and exact dollar savings per Claude session.

---

##  Real-World Codebase Context Benchmarks

Estimate the impact of AST code compression on large open-source repositories (calculated at $3.00 / million input tokens for Claude 3.5 Sonnet):

|  Repository |  Total Files |  Raw Context Tokens |  Compressed Context Tokens |  Token Reduction |  Cost Saved / Run |
| :--- | :---: | :---: | :---: | :---: | :---: |
| **Django** | 2,359 | 5,554,607 | 596,752 | **89.3%** | **$10.99** |
| **Spring Framework** | 9,193 | 15,032,871 | 5,185,299 | **65.5%** | **$29.03** |
| **Next.js** | 21,985 | 23,963,330 | 10,212,684 | **57.4%** | **$45.88** |

*Reproducible: Clone any of these repositories locally and run `mcp-benchmark` on them.*

---

##  Install CLI Tool

Install the standalone command-line benchmark tool:

```bash
curl -fsSL https://raw.githubusercontent.com/foldwork-dev/mcp-benchmark/main/install.sh | sh
```

##  Usage

Run the scanner on any local codebase directory:

```bash
mcp-benchmark ./your-project
```

## Example Output

```text
mcp-benchmark ./context

════════════════════════════════════════════════════════════════════════════════
  mcp-benchmark - context
  Tier 3 compression  |  $3.00/1M tokens  |  2026-07-06T12:00:00Z
════════════════════════════════════════════════════════════════════════════════

FILE                                          RAW TOKENS    COMPRESSED     SAVED   COST SAVED*
──────────────────────────────────────────────────────────────────────────────────────────
cmd/license-gen/main.go                            3,633           214     94.1%       $0.0103
main.go                                           17,555         1,917     89.1%       $0.0469
website/api/webhook.go                             2,682           295     89.0%       $0.0072
main_test.go                                       1,576           353     77.6%       $0.0037
──────────────────────────────────────────────────────────────────────────────────────────
TOTAL (4 files)                                   25,446         2,779     89.1%       $0.0680

  * Based on $3.00 / 1M input tokens

  Running this codebase through Claude 10x/day costs $0.76/day raw.
  With mcp-injector:  $0.08/day.  You save $0.68/day ($20/month).
```

This is the output from running mcp-benchmark on the mcp-injector source code itself. The numbers shown are real, not illustrative.

---

##  How It Works

`mcp-benchmark` walks your directory tree and simulates the local code compiler parser, measuring:
- **Tier 2 AST Body Folding:** Strips class methods and function blocks while keeping class signatures, imports, and interface definitions fully intact.
- **Tier 3 Comment Stripping:** Strips docstrings, inline comments, and package meta comments on top of Tier 2.

### Under the Hood
* **Token Heuristic:** standard `bytes / 3.5` code-to-token ratio.
* **Pricing Calculator:** Custom rates configurable with `--price-per-million` (defaults to Claude 3.5 Sonnet pricing).
* **Language Support:** Go, Python, TypeScript, JavaScript, Java, C++, Rust.

---

##  Want the Full MCP Daemon?

`mcp-benchmark` is the measurement tool. **[mcp-injector](https://foldwork.dev)** is the persistent local Model Context Protocol (MCP) server daemon that automatically integrates with **Claude Desktop**, **Cursor IDE**, **VS Code**, and **Devin Desktop** to compress files live and support Compress-Cache-Retrieve (CCR) fetching.

 Learn more at **[foldwork.dev](https://foldwork.dev)**

##  License

MIT
