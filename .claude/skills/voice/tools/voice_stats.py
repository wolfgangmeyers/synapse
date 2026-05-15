#!/usr/bin/env python3
"""Compute quantitative voice metrics from JSONL records.

Reads JSONL records (one per line, each with a "text" field) from stdin or
files and outputs a JSON object with statistical voice fingerprint data.

Usage:
    python3 voice_stats.py < chunks.jsonl > stats.json
    python3 voice_stats.py chunks.jsonl > stats.json
"""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from collections import Counter
from typing import TextIO


# ---------------------------------------------------------------------------
# Sentence splitting
# ---------------------------------------------------------------------------

_SENTENCE_RE = re.compile(
    r"(?<=[.!?])\s+(?=[A-Z])"  # split after terminal punctuation before caps
    r"|(?<=[.!?])$",            # or at end of text
)

_ABBREVS = {"mr.", "mrs.", "ms.", "dr.", "prof.", "sr.", "jr.", "vs.", "etc.",
             "e.g.", "i.e.", "fig.", "approx.", "dept.", "est.", "vol."}


def _split_sentences(text: str) -> list[str]:
    """Split text into sentences, handling common abbreviations."""
    raw = _SENTENCE_RE.split(text.strip())
    sentences = []
    buf = ""
    for part in raw:
        part = part.strip()
        if not part:
            continue
        if buf:
            buf += " " + part
        else:
            buf = part
        # Check if it ends with an abbreviation rather than a real sentence end
        lower = buf.lower()
        if any(lower.endswith(a) for a in _ABBREVS):
            continue
        sentences.append(buf)
        buf = ""
    if buf:
        sentences.append(buf)
    return [s for s in sentences if s]


# ---------------------------------------------------------------------------
# Word tokenization
# ---------------------------------------------------------------------------

_WORD_RE = re.compile(r"[a-zA-Z]+(?:'[a-zA-Z]+)?")


def _tokenize(text: str) -> list[str]:
    return _WORD_RE.findall(text.lower())


# ---------------------------------------------------------------------------
# Fragment detection (sentences without a main verb — heuristic)
# ---------------------------------------------------------------------------

_COMMON_VERBS = {
    "is", "are", "was", "were", "be", "been", "being", "am",
    "have", "has", "had", "do", "does", "did", "will", "would",
    "shall", "should", "may", "might", "can", "could", "must",
    "get", "got", "go", "went", "gone", "come", "came", "take",
    "took", "taken", "make", "made", "know", "knew", "known",
    "think", "thought", "say", "said", "see", "saw", "seen",
    "want", "need", "feel", "felt", "look", "looked", "find",
    "found", "give", "gave", "tell", "told", "work", "worked",
    "call", "called", "try", "tried", "ask", "asked", "use",
    "used", "keep", "kept", "let", "put", "seem", "seemed",
    "help", "helped", "show", "showed", "hear", "heard", "play",
    "played", "run", "ran", "move", "moved", "live", "lived",
    "believe", "believed", "happen", "happened", "write", "wrote",
    "written", "provide", "provided", "read", "stand", "stood",
    "lose", "lost", "pay", "paid", "meet", "met", "include",
    "included", "continue", "continued", "set", "learn", "learned",
    "change", "changed", "lead", "led", "understand", "understood",
    "watch", "watched", "follow", "followed", "stop", "stopped",
    "create", "created", "speak", "spoke", "spoken", "start",
    "started", "remember", "remembered", "love", "loved", "like",
    "liked", "hate", "hated",
}

# Common verb suffixes for catching conjugated forms
_VERB_SUFFIXES = ("ing", "ed", "ize", "ise", "ify", "ate")


def _has_verb(sentence: str) -> bool:
    words = _tokenize(sentence)
    for w in words:
        if w in _COMMON_VERBS:
            return True
        if any(w.endswith(s) for s in _VERB_SUFFIXES) and len(w) > 4:
            return True
    return False


# ---------------------------------------------------------------------------
# Stopwords for content word extraction
# ---------------------------------------------------------------------------

_STOPWORDS = {
    "the", "a", "an", "and", "or", "but", "in", "on", "at", "to", "for",
    "of", "with", "by", "from", "is", "are", "was", "were", "be", "been",
    "being", "have", "has", "had", "do", "does", "did", "will", "would",
    "shall", "should", "may", "might", "can", "could", "must", "that",
    "this", "these", "those", "it", "its", "i", "me", "my", "we", "our",
    "you", "your", "he", "she", "they", "them", "his", "her", "their",
    "what", "which", "who", "whom", "where", "when", "why", "how",
    "not", "no", "nor", "if", "then", "than", "so", "as", "just",
    "about", "up", "out", "all", "some", "any", "each", "every",
    "into", "over", "after", "before", "between", "through", "during",
    "there", "here", "also", "only", "very", "too", "more", "most",
    "much", "many", "well", "still", "already", "even", "back",
    "now", "really", "like", "don't", "didn't", "it's", "i'm",
    "that's", "don", "didn", "won", "got", "get",
}


# ---------------------------------------------------------------------------
# Percentile helper
# ---------------------------------------------------------------------------

def _percentile(sorted_values: list[float], p: float) -> float:
    if not sorted_values:
        return 0.0
    k = (len(sorted_values) - 1) * (p / 100.0)
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return sorted_values[int(k)]
    return sorted_values[f] * (c - k) + sorted_values[c] * (k - f)


# ---------------------------------------------------------------------------
# Punctuation counting
# ---------------------------------------------------------------------------

def _count_punctuation(text: str) -> dict[str, int]:
    return {
        "em_dash": text.count("\u2014") + text.count("--"),
        "ellipsis": text.count("\u2026") + len(re.findall(r"\.{3}", text)),
        "exclamation": text.count("!"),
        "question": text.count("?"),
        "parenthetical": text.count("("),
        "semicolon": text.count(";"),
        "colon": text.count(":"),
    }


# ---------------------------------------------------------------------------
# Opening / closing word analysis
# ---------------------------------------------------------------------------

def _opening_words(sentences: list[str], n: int = 2) -> Counter:
    counter: Counter = Counter()
    for s in sentences:
        words = _tokenize(s)[:n]
        if words:
            counter[" ".join(words)] += 1
    return counter


def _closing_words(sentences: list[str], n: int = 2) -> Counter:
    counter: Counter = Counter()
    for s in sentences:
        words = _tokenize(s)[-n:]
        if words:
            counter[" ".join(words)] += 1
    return counter


# ---------------------------------------------------------------------------
# Main analysis
# ---------------------------------------------------------------------------

def analyze(records: list[dict]) -> dict:
    all_sentences: list[str] = []
    all_words: list[str] = []
    paragraph_lengths: list[int] = []
    punctuation_totals: dict[str, int] = Counter()
    record_count = len(records)

    for rec in records:
        text = rec.get("text", "")

        paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
        for para in paragraphs:
            words_in_para = _tokenize(para)
            paragraph_lengths.append(len(words_in_para))

        sentences = _split_sentences(text)
        all_sentences.extend(sentences)

        words = _tokenize(text)
        all_words.extend(words)

        punct = _count_punctuation(text)
        for k, v in punct.items():
            punctuation_totals[k] += v

    total_words = len(all_words)
    unique_words = set(all_words)
    total_sentences = len(all_sentences)

    # Sentence length distribution (in words)
    sentence_lengths = sorted(len(_tokenize(s)) for s in all_sentences)

    # Vocabulary richness
    ttr = len(unique_words) / total_words if total_words else 0.0
    word_freq = Counter(all_words)
    hapax = sum(1 for w, c in word_freq.items() if c == 1)
    hapax_ratio = hapax / len(unique_words) if unique_words else 0.0

    # Fragment ratio
    fragments = sum(1 for s in all_sentences if not _has_verb(s))
    fragment_ratio = fragments / total_sentences if total_sentences else 0.0

    # Questions ratio
    question_sentences = sum(1 for s in all_sentences if s.rstrip().endswith("?"))
    question_ratio = question_sentences / total_sentences if total_sentences else 0.0

    # Punctuation per 1000 words
    punct_per_1k = {}
    for k, v in punctuation_totals.items():
        punct_per_1k[k] = round(v / total_words * 1000, 2) if total_words else 0.0

    # Paragraph length distribution
    para_lengths_sorted = sorted(paragraph_lengths)

    # Opening / closing patterns
    top_openings = _opening_words(all_sentences, n=2).most_common(20)
    top_closings = _closing_words(all_sentences, n=2).most_common(20)

    # Most frequent words (excluding very common stopwords)
    content_words = Counter(w for w in all_words if w not in _STOPWORDS and len(w) > 2)
    top_content_words = content_words.most_common(30)

    # Average words per sentence
    avg_sentence_len = total_words / total_sentences if total_sentences else 0.0

    return {
        "record_count": record_count,
        "total_words": total_words,
        "unique_words": len(unique_words),
        "total_sentences": total_sentences,
        "sentence_length": {
            "min": sentence_lengths[0] if sentence_lengths else 0,
            "p25": round(_percentile(sentence_lengths, 25), 1),
            "median": round(_percentile(sentence_lengths, 50), 1),
            "p75": round(_percentile(sentence_lengths, 75), 1),
            "max": sentence_lengths[-1] if sentence_lengths else 0,
            "mean": round(avg_sentence_len, 1),
        },
        "vocabulary": {
            "type_token_ratio": round(ttr, 4),
            "hapax_ratio": round(hapax_ratio, 4),
            "top_content_words": top_content_words,
        },
        "punctuation_per_1000_words": punct_per_1k,
        "fragment_ratio": round(fragment_ratio, 4),
        "question_ratio": round(question_ratio, 4),
        "paragraph_length": {
            "min": para_lengths_sorted[0] if para_lengths_sorted else 0,
            "p25": round(_percentile(para_lengths_sorted, 25), 1),
            "median": round(_percentile(para_lengths_sorted, 50), 1),
            "p75": round(_percentile(para_lengths_sorted, 75), 1),
            "max": para_lengths_sorted[-1] if para_lengths_sorted else 0,
        },
        "opening_bigrams": top_openings,
        "closing_bigrams": top_closings,
    }


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def _read_jsonl(stream: TextIO) -> list[dict]:
    records = []
    for line in stream:
        line = line.strip()
        if not line:
            continue
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError:
            print(f"Warning: skipping malformed JSONL line: {line[:80]!r}", file=sys.stderr)
    return records


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="*", help="JSONL files (default: stdin)")
    parser.add_argument("-o", "--output", help="Output file (default: stdout)")
    args = parser.parse_args()

    records: list[dict] = []
    if args.files:
        for path in args.files:
            with open(path) as f:
                records.extend(_read_jsonl(f))
    else:
        records = _read_jsonl(sys.stdin)

    if not records:
        print("Error: no records to analyze", file=sys.stderr)
        return 1

    stats = analyze(records)

    output = json.dumps(stats, indent=2, ensure_ascii=False)
    if args.output:
        with open(args.output, "w") as f:
            f.write(output + "\n")
    else:
        print(output)

    return 0


if __name__ == "__main__":
    sys.exit(main())
