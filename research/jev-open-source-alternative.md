# Jev and Its Claimed Open-Source Alternatives: Identity Reconciliation

**Research date:** 2026-09-19 UTC  
**Question resolved:** Is [`github.com/vinnylarouge/jevlike`](https://github.com/vinnylarouge/jevlike) the project linked by [Reddit post `1wijo3e`](https://www.reddit.com/r/LocalLLaMA/comments/1wijo3e/i_literally_built_the_jev_architecture_one_year/)?

## Bottom line

**No. Jevlike is not the project linked by the Reddit post.** Reddit's live embed HTML exposes the post body and its outbound links. The post links to:

1. **SalesRLAgent / DeepMost**, a sales-conversion probability project:
   - [paper, arXiv:2503.23303](https://arxiv.org/abs/2503.23303)
   - [model and code on Hugging Face](https://huggingface.co/DeepMostInnovations/sales-conversion-model-reinf-learning)
   - [synthetic sales dataset](https://huggingface.co/datasets/DeepMostInnovations/saas-sales-conversations)
   - [a second paper, arXiv:2510.01237](https://arxiv.org/abs/2510.01237)
2. A later update in the same post links to **Laya**, the Reddit author's subsequent generic decision model. The corresponding follow-up post names [GitHub `NandhaKishorM/laya`](https://github.com/NandhaKishorM/laya), [Hugging Face `convaiinnovations/laya`](https://huggingface.co/convaiinnovations/laya), and the [Laya demo](https://huggingface.co/spaces/convaiinnovations/laya-demo).

The Reddit body contains **no link or reference to Jevlike**. Jevlike describes itself instead as an **independent starter model** implementing a similar input/output shape, explicitly says TypeSafe has not published Jev's design, and explicitly says it did not show equal quality with Jev. It was created under a different GitHub account, `vinnylarouge`.

### Practical verdict

- **Original Reddit-linked SalesRLAgent:** real public artifacts, but a narrow sales-trajectory predictor, not a drop-in Jev alternative.
- **Same-author follow-up Laya:** the closest actual open-weight Jev-style candidate found. It is runnable in principle and permissively licensed, but its Jev comparisons are not controlled or independently reproduced; its released evidence has important gaps.
- **Jevlike:** a legitimate MIT-licensed educational/research starter with the same broad “context + variable options → probabilities” shape, but unrelated to the Reddit claim and not demonstrated as a Jev replacement.
- **Jev:** a proprietary hosted API with the best-documented production contract of the three, but no public weights, training implementation, architecture paper, or reproducible public benchmark against these alternatives.

Do not adopt any of the open projects as a production-equivalent Jev substitute based on headline benchmark tables alone. Evaluate Jev and the candidate on the same private test set, prompts, option sets, calibration metrics, and hardware.

---

## How identity was established

### Primary Reddit evidence

Reddit's [oEmbed endpoint](https://www.reddit.com/oembed?url=https://www.reddit.com/r/LocalLLaMA/comments/1wijo3e/) confirms:

- author: `u/Nandakishor_ml`;
- title: “I literally built the Jev architecture one year back and completely open-sourced it with model, dataset and paper”; and
- canonical post ID: `1wijo3e`.

More importantly, Reddit's [live embed page](https://embed.reddit.com/r/LocalLLaMA/comments/1wijo3e/i_literally_built_the_jev_architecture_one_year/) renders the full post body even when ordinary Reddit JSON/HTML requests return access blocks. The rendered body directly contains the DeepMost Hugging Face and arXiv links listed above. It says the original model uses “PPO over sequence embeddings to output turn-by-turn conversion trajectories.” That is decisive outbound-link evidence.

The same embed body now starts with an update linking a follow-up Reddit post. The author's follow-up submission identifies the new project as **Laya** and links the `NandhaKishorM/laya` GitHub repository and `convaiinnovations/laya` Hugging Face model.

### Corroborating history

Archived submission metadata for `u/Nandakishor_ml` preserves:

- the original May 2025 LocalLLaMA post [`1kl0uvv`](https://www.reddit.com/r/LocalLLaMA/comments/1kl0uvv/predicting_sales_conversion_probability_from/), which links the same DeepMost model, dataset, and paper;
- later posts linking the [DeepMost Python repository](https://github.com/DeepMostInnovations/deepmost) and [PyPI package](https://pypi.org/project/deepmost/); and
- the September 2026 Laya follow-up linking `NandhaKishorM/laya`.

This archive is secondary corroboration, not the basis of identity; the live Reddit embed itself supplies the exact outbound links.

### Why the Jevlike assumption fails

| Evidence | Reddit-linked work | Jevlike |
|---|---|---|
| Link appears in live Reddit body | Yes: DeepMost artifacts, two papers, later Laya update | **No** |
| Account/project identity | Reddit history links Nandakishor/DeepMost/Convai artifacts | GitHub owner `vinnylarouge`; package author “Minimal Labs” |
| Original domain | Sales conversion probability over conversation turns | Generic option scorer plus Doom/chess experiments |
| Self-description | Author claims architectural similarity to Jev | “Independent starter model with the same input and output shape”; “not a copy of Jev” |
| Jev-equivalence claim | Claimed by the Reddit author; not established by controlled evidence | README expressly says it did **not** show equal quality or reproduce TypeSafe's private training method |

A repository name and search-engine proximity are not provenance. Direct outbound links control here.

---

## A. The exact Reddit-linked project

## A1. Original linked work: SalesRLAgent / DeepMost

### What it actually is

The [model card](https://huggingface.co/DeepMostInnovations/sales-conversion-model-reinf-learning) describes a Stable-Baselines3 PPO model that combines conversation embeddings and LLM-derived sales metrics to produce a **conversion probability after each conversation turn**. The recommended open configuration uses:

- `BAAI/bge-m3` embeddings (1,024 dimensions);
- a local `Qwen3-4B-GGUF` model to derive dynamic engagement/effectiveness metrics; and
- a PPO policy producing a continuous value between 0 and 1.

This is not the same contract as Jev. It does not accept an arbitrary set of named `choice`, `score`, and boolean questions and score all supplied options in one general forward pass. It predicts a sales-specific trajectory. It may illustrate a related idea—non-autoregressive probabilistic decision support—but it is **not a drop-in Jev implementation**.

The second linked paper, [“Confidence-Aware Routing for Large Language Model Reliability Enhancement”](https://arxiv.org/abs/2510.01237), concerns pre-generation hallucination routing using multiple confidence signals. Its title and abstract do not establish that it implements Jev's typed, variable-schema API either.

### Artifact and license audit

| Artifact | Availability | License / caveat |
|---|---|---|
| [HF model repository](https://huggingface.co/DeepMostInnovations/sales-conversion-model-reinf-learning) | Public; includes model ZIPs, `train.py`, and inference scripts | Declares **MIT** |
| [DeepMost package source](https://github.com/DeepMostInnovations/deepmost) | Public Python package; model downloader and inference wrapper | **MIT** |
| [PyPI `deepmost`](https://pypi.org/project/deepmost/) | Public beta package | Metadata declares MIT |
| [Synthetic sales dataset](https://huggingface.co/datasets/DeepMostInnovations/saas-sales-conversations) | Public CSV; one train set | Declares **Apache-2.0** |
| [Open BGE variant](https://huggingface.co/datasets/DeepMostInnovations/saas-sales-bge-open) | Public sharded Parquet; card still says upload is incomplete | No clear license tag observed in repository metadata; verify before use |
| [SalesRLAgent paper](https://arxiv.org/abs/2503.23303) | Public arXiv manuscript | A paper is documentation, not a software/weight license |

The model, package, dataset, embedding model, GGUF LLM, and any Azure-generated material have separate terms. MIT on the model repository does not override the licenses or service terms of BGE, Qwen, Azure OpenAI, or the dataset.

### Benchmarks and evidence quality

The model card reports:

- 96.7% conversion-prediction accuracy;
- 85 ms inference versus 3,450 ms for GPT-4;
- 34.7% improvement over an LLM-only approach; and
- a 43.2% conversion-rate increase when used by sales representatives.

These should be treated as **author-reported**, not independently reproduced. Material limitations include:

- the main dataset card says the data are entirely synthetic, generated with Azure OpenAI GPT-4;
- the dataset is published as a single train set and instructs users to create their own splits;
- outcomes, engagement scores, effectiveness scores, and probability trajectories are automatically generated;
- no controlled head-to-head evaluation with Jev exists;
- “accuracy” for a continuous probability predictor is not sufficiently defined by the model card alone; and
- end-to-end latency depends on the embedding model and auxiliary LLM, not just the small PPO policy.

### Viability

**Research/demo viability: moderate. Production replacement viability: low.** The release is more complete than the earlier review concluded: code, weights, data, paper, package, and licenses do exist. However, the workload and interface differ fundamentally from Jev, and the quality claims rest on synthetic data and author evaluation. It is suitable for reproducing a sales-specific prototype, not for claiming general Jev parity.

## A2. Same-post update: Laya

The post was later updated to say the author built a generic model that beat Jev's benchmarks. The linked follow-up calls it **Laya**.

### Resources and architecture

- [GitHub](https://github.com/NandhaKishorM/laya)
- [Hugging Face model family](https://huggingface.co/convaiinnovations/laya)
- [Demo](https://huggingface.co/spaces/convaiinnovations/laya-demo)
- [Benchmark narrative](https://github.com/NandhaKishorM/laya/blob/main/BENCHMARKS.md)
- [PyPI](https://pypi.org/project/laya/)

The release describes three checkpoints:

- English: ModernBERT-large, 421M parameters, 512-token context;
- multilingual: mmBERT-base, 322M parameters, 1,024-token context; and
- typed-decisions: 421M parameters, fine-tuned on the typed-decisions workflows.

It implements Jev-like `choice`, `score`, and `noul` outputs, uses option-marker scoring in one forward pass, publishes safetensors weights, and declares **Apache-2.0** for code and model repositories. This is much closer to a practical open Jev-style substitute than SalesRLAgent.

### What the reported numbers do—and do not—show

Laya reports, among other results:

- 32.8–39.5 ms p50 for one question on a Tesla T4;
- 0.766 accuracy on 2,000 typed decisions for its task-fine-tuned checkpoint;
- 0.950 on AG News and 0.595 on DAIR Emotion for routed checkpoints;
- post-temperature ECE of 0.081; and
- weaker Banking77 performance (0.425) due to its shared option-token budget.

The repository is unusually candid about several limitations:

- base checkpoints score about 0.34–0.36 on typed-decisions, below a 0.461 majority baseline;
- the 0.766 result belongs to a checkpoint fine-tuned on that benchmark's training split;
- moderation is 0.530 / macro-F1 0.400 on held-out data;
- models ship overconfident and require domain temperature fitting;
- English and multilingual checkpoints fail differently; and
- default choice questions should stay below roughly 20 options.

The claimed win over Jev is **not established**:

1. Laya's README says the Jev figures are third-party published, were never measured by the Laya author, and use different samples and prompts.
2. TypeSafe's own launch FAQ says it deliberately does **not** publish public-benchmark performance. That makes Laya's label “Jev (published)” misleading unless each third-party measurement and harness is traced precisely.
3. The cited third-party latency repositories are not the same as a controlled run on the same T4 and network conditions.
4. At the inspected Git commit, `BENCHMARKS.md` refers to `research/results/*.json`, but those files are absent from the repository tree. The README also refers to a benchmark Colab notebook that is not present under that filename; the released notebook is a fine-tuning notebook. Aggregate tables and images therefore are not a complete reproducibility bundle.
5. The post says the training corpus contains more than 25,000 human-annotated examples, but no corresponding training-dataset release or auditable data license was located.

**Viability:** promising beta/research candidate, especially for a narrow fine-tuned workflow. Before production, require the missing evaluation inputs/scripts, verify the complete training-data provenance, reproduce on clean hardware, and test calibration on the intended domain. Apache-2.0 code and weights are helpful, but they do not resolve the unreleased training-data provenance.

---

## B. Jevlike is a separate project

[Jevlike](https://github.com/vinnylarouge/jevlike) is an MIT-licensed Python/PyTorch repository created independently by `vinnylarouge` / “Minimal Labs.” Its README is careful about scope:

> “TypeSafe has not published its design. This repository is an independent starter model with the same input and output shape.”

It encodes each candidate option as a query, attends over context tokens, assigns one score per option, and softmaxes across options. The default model learns byte embeddings from scratch; an optional path uses a frozen Hugging Face encoder such as Qwen2.5-0.5B.

### Released resources

- package source, CLI training/evaluation/prediction commands, and tests;
- synthetic menu-data generator;
- Wikispeedia preparation script (data stay external under their own terms);
- supplied Doom and chess controller checkpoints; and
- reproducible small examples for CPU, Apple MPS, or CUDA-capable PyTorch.

### Reported experiments

The README reports local experiments of approximately:

- 98% accuracy on synthetic menus;
- 26% Wikispeedia next-click accuracy with a frozen Qwen2.5-0.5B encoder, versus about 8% shuffled/random controls;
- 29% Wikispeedia accuracy for a small from-scratch model trained on 40,000 clicks; and
- roughly 100× speed versus a small decoder forced to generate 400 tokens at eight options.

The README explicitly warns that these numbers describe local experiments, not the quickstart; the speed baseline is not Jev; and the project did not reproduce TypeSafe's method or equal quality. Its game results also show a learned controller rather than strong gameplay (for example, the chess checkpoint loses 48/50 games to Stockfish level 0).

### License and viability

- **Code:** MIT.
- **Bundled checkpoints:** distributed in the MIT repository, but users should still verify whether any training inputs impose additional terms.
- **External encoders and Wikispeedia:** retain their own licenses and data terms.

**Viability:** good as a compact educational baseline and architecture playground; low as a general Jev alternative. There is no pretrained general-purpose semantic decision checkpoint or Jev-quality comparison. Its default 192-byte context and 32-byte option truncation are also far below Jev's production context limits.

---

## C. Verified Jev facts

The following come from TypeSafe's [official launch post](https://typesafe.ai/blog/introducing-system-one-models-and-jev), [model documentation](https://docs.typesafe.ai/models), [API reference](https://docs.typesafe.ai/api), [confidence documentation](https://docs.typesafe.ai/confidence), and [Jev 1.13 limitations page](https://docs.typesafe.ai/model-jaggedness/jev-1.13).

### Product and interface

- Current documented version: `jev-1.13.0`; `jev-latest` and `jev-preview` pointed to it on the research date.
- Hosted endpoint: `POST https://api.typesafe.ai/v1/systemone`.
- Input: text, including strings or JSON objects/arrays of text values; no direct image, audio, or video input.
- Three question primitives:
  - **Choice:** selected option, a probability for every option (summing to 1), and derived confidence;
  - **Score:** probability distribution over ordered levels, probability-weighted score, and confidence;
  - **Noul:** probability that a yes/no statement is true.
- Multiple questions are evaluated against one state in parallel.
- The launch post says Jev supports Choice cardinality up to 255, using a two-stage system for higher-cardinality choices.

### Service envelope

As documented for Jev 1.13:

- price: **$42 per billion input tokens / $0.042 per million**; output tokens are free;
- rate limits: **250,000 tokens/second and 1,200 requests/minute**, explicitly described as dynamically adjustable;
- context: **64k tokens per request**, with **32k** for state plus the longest single question;
- English is the primary and strongest language; other languages require workload testing; and
- TypeSafe says customer requests/responses are not used for training, with enterprise zero-data-retention terms referenced separately.

These are hosted-service specifications, not proof of equivalent measured performance in every region or workload.

### Architecture, training, and benchmark disclosure

Verified public claims are limited:

- TypeSafe calls the training approach **Reinforcement Learning for Calibrated Decisions (RLCD)** and says Jev uses a new architecture and parallel sampler.
- It describes Jev as producing typed probabilistic decisions rather than generated strings.
- It claims 70–500 ms end-to-end time and 40–200× speedups for comparable System One queries; its workflow example is the source of higher-end 193.6× speed / 444.6× cost claims.
- Its workflow evaluations use the average predictions of large external models as reference probabilities, not necessarily ground-truth labels. TypeSafe itself discloses potential harness-author bias and provider-routing bias.
- The launch FAQ explicitly says TypeSafe chose **not to publish public-benchmark performance**.
- No public Jev weights, training code, detailed architecture specification, or technical paper was located.

Therefore, third-party tables assigning Jev exact AG News, DAIR Emotion, Banking77, ECE, or typed-decisions values are **third-party measurements**, not official comparable benchmarks. They must be audited at the harness and raw-prediction level before use.

### “Cannot hallucinate” needs qualification

TypeSafe guarantees output-schema conformance, not semantic correctness. The official limitations page documents failures involving literal readings, math/counting, dates, indirection, irrelevant long context, adversarial content, contradictory criteria, and non-invariance between equivalent question forms. It also says separate probabilities need not satisfy intuitive arithmetic identities.

The defensible statement is: **Jev cannot emit an out-of-schema response for its defined primitives, but it can still make wrong, inconsistent, or poorly framed decisions.**

### Jev licensing and deployment

Jev is a proprietary hosted service. TypeSafe publishes client/adapter code, documentation, and site terms, but no license for self-hosting model weights because no weights are public. The site terms grant only a limited service/site license and reserve proprietary rights. Any production evaluation should also review the applicable API/customer agreement, privacy terms, data-processing agreement, retention guarantees, and availability commitments—not merely the public website terms.

---

## Controlled comparison required before adoption

Use a single harness for Jev, Laya, Jevlike, and any task-specific model:

1. Freeze a private, representative test set before tuning.
2. Use byte-identical state, instructions, criteria, and candidate order.
3. Test option permutations, missing-correct-option cases, duplicates, and distractors.
4. Report top-1 accuracy, macro-F1, log loss, Brier score, ECE with binning details, and selective risk/coverage.
5. Separate zero-shot, in-training-domain, and task-fine-tuned results.
6. Record p50/p95 latency, cold start, batch size, hardware, network region, memory, and total dependency cost.
7. Preserve raw predictions and exact model/repository revisions.
8. Audit licenses separately for code, weights, training data, evaluation data, and upstream encoders.

### Recommendation by use case

| Need | Best next step |
|---|---|
| Production hosted typed decisions now | Trial Jev under a private evaluation and commercial/legal review |
| Self-hosted general typed decisions | Reproduce Laya first; plan to fine-tune and recalibrate on the actual workload |
| Architecture education or small experiments | Jevlike is the cleanest minimal starter |
| Sales conversation trajectory prediction | Evaluate DeepMost on real, consented sales data; do not infer general Jev equivalence |
| Evidence of “beats Jev” | None of the reviewed public evidence is sufficient; run a controlled head-to-head |

---

## Source index

### Identity and exact Reddit links

- [Canonical Reddit post](https://www.reddit.com/r/LocalLLaMA/comments/1wijo3e/i_literally_built_the_jev_architecture_one_year/)
- [Reddit oEmbed metadata](https://www.reddit.com/oembed?url=https://www.reddit.com/r/LocalLLaMA/comments/1wijo3e/)
- [Reddit embed rendering with full outbound-linked body](https://embed.reddit.com/r/LocalLLaMA/comments/1wijo3e/i_literally_built_the_jev_architecture_one_year/)
- [Reddit author's original 2025 project post](https://www.reddit.com/r/LocalLLaMA/comments/1kl0uvv/predicting_sales_conversion_probability_from/)

### Reddit-linked artifacts

- [SalesRLAgent model/code](https://huggingface.co/DeepMostInnovations/sales-conversion-model-reinf-learning)
- [Sales dataset](https://huggingface.co/datasets/DeepMostInnovations/saas-sales-conversations)
- [Open BGE dataset variant](https://huggingface.co/datasets/DeepMostInnovations/saas-sales-bge-open)
- [DeepMost package source](https://github.com/DeepMostInnovations/deepmost)
- [SalesRLAgent paper](https://arxiv.org/abs/2503.23303)
- [Confidence-aware routing paper](https://arxiv.org/abs/2510.01237)
- [Laya source](https://github.com/NandhaKishorM/laya)
- [Laya model card and weights](https://huggingface.co/convaiinnovations/laya)
- [Laya benchmark report](https://github.com/NandhaKishorM/laya/blob/main/BENCHMARKS.md)

### Separate Jevlike project

- [Jevlike source and README](https://github.com/vinnylarouge/jevlike)
- [Jevlike Doom example](https://github.com/vinnylarouge/jevlike/tree/main/examples/doom)
- [Jevlike chess example](https://github.com/vinnylarouge/jevlike/tree/main/examples/chess)

### Official Jev evidence

- [TypeSafe launch post](https://typesafe.ai/blog/introducing-system-one-models-and-jev)
- [Official model specifications and pricing](https://docs.typesafe.ai/models)
- [Official API contract](https://docs.typesafe.ai/api)
- [Official primitives](https://docs.typesafe.ai/primitives)
- [Official confidence semantics](https://docs.typesafe.ai/confidence)
- [Official Jev 1.13 limitations](https://docs.typesafe.ai/model-jaggedness/jev-1.13)
- [TypeSafe workflow evaluations](https://evals.typesafe.ai/)
- [TypeSafe terms](https://typesafe.ai/legal/terms)

## Remaining uncertainty

The identity question is resolved by direct Reddit outbound-link evidence. Remaining uncertainty concerns **quality**, not identity:

- no independent reproduction of DeepMost or Laya was located;
- Laya's raw training corpus and complete benchmark harness/results were not available in the inspected repository snapshot;
- third-party Jev measurements are not interchangeable with official claims or with one another;
- Jev's internal architecture, weights, and training data remain private; and
- repository metadata, download counts, prices, limits, aliases, and live post edits can change after this research date.
