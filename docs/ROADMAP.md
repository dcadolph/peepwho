# Roadmap

WhoToAsk answers "who do I talk to about X" from the data it has indexed. These are
the changes planned next for the core.

## Engine

- Memory-mapped vector store: embeddings are int8-quantized in the index today,
  a quarter of their float32 size; a separate memory-mapped store would let a
  very large graph search without loading every vector into memory.

## Experience

- Result deep links: open a channel or a profile directly from an answer.
