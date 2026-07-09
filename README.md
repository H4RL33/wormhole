![Wormhole
Wordmark](https://github.com/H4RL33/wormhole/blob/main/brand/wordmark_bws_ow.jpg)

# Wormhole

Persistent organisational infrastructure, built for AI agents first and humans second.

Code is versioned by Git. Organisations are versioned by Wormhole. Wormhole combines a structured event bus (communication), a task graph (coordination), and a linked knowledge graph (organisational memory), all exposed through MCP so any compliant agent — Claude, Codex, Gemini, or otherwise — can read and write to the same shared context.

## What is Wormhole?

![Lo-fi Excalidraw Diagram showing that Wormhole centralises AI agent
knowledge, context, and skills onto a server-side system where any agent can
connect to and get the same
data](https://github.com/H4RL33/wormhole/blob/main/docs/diagrams/excalidraw_overview_08082026.jpeg)

## Who is Wormhole for?

Wormhole can be deployed by anyone, and even a solo developer can see improvements to their agents' work as the models can rely less on their own context windows and can save important information to the Wormhole instance.

Furthermore, for solo developers who use multiple models, Wormhole can alleviate the usual pains of instructing new agents to gather context about a codebase.

For SMEs, Wormhole becomes something far greater; it allows your developers' agents from across your organisation to communicate and collaborate in real-time, elevating agents from developer-accelerators and per-developer tools to native members of your team.

## Goals for Wormhole

Wormhole is built based on one observation:

LLMs are becoming incredibly good at coding, compared to just a few years ago where a simple shell script could have errors; many are now able to create full-stack applications with just a few turns.

The Wormhole project believes that agents are now reaching bottlenecks elsewhere in the layer around the model. Models themselves are stateless, you shoot vectors in and you get vectors out, therefore the model itself should be interchangeable - like a car engine, an I4 could be swapped for a V8 if the chassis permits (and the supporting systems can handle it).

This is why Wormhole is model-agnostic, leading to the entire app being used through MCP, an open and widely-adopted standard. The value of Wormhole comes not from the models that plug into it, but from the layer itself.

Wormhole aims to share the workload of a model, acting as a foundation layer for it to operate off of. We believe that models are reaching the upper limit of vertical scaling, and that new frontlines for agentic research are emerging.

### The Social Good

We are not oblivious to the sentiment towards generative AI, the environmental impact, the financial situation, and the concerns around proprietary black-box models.

Part of the goal of Wormhole is to alleviate the workload of agentic coders, allowing them to gather context more efficiently, and produce better quality output in fewer turns and to improve the output of lower-parameter models and SLMs.

We don't believe that smaller models are less-capable, we believe that they just need a holding-hand that larger models simply scale-out.

Open-source, open-weight models will always be our first-class citizens, proprietary models we will support simply because we cannot be oblivious to their out-of-box better output, however we will not officially support models that we believe on a case-by-case basis come from providers that harm society.

To that extent, we will officially provide Claude Code connectors only. Gemini nor OpenAI models will never see official support in Wormhole.

Anthropic is a fear-mongering organisation, but we sincerely believe that they have the good of the people at least in the same postcode as their heart; Gemini and OpenAI come from mass surveilleing, data farming, double-crossing, and genocide-enabling providers.

#### Being Open-Source

Wormhole will always remain open-source, as we believe that all products in the AI-space should be.

Because of that, it is relatively trivial to create third-party connectors to other platforms.

We state that we will never officially support the aforementioned providers, however it would be impossible for us to stop the development of community connectors for these platforms; so simply, if one was made, go ahead and use it.

Furthermore, we reiterate that Wormhole is built on-top of the MCP, which is an open protocol and model-agnostic (all the model needs is the ability to use tools).

All we can do is encourage you to reconsider your provider of choice.

## Status

Pre-alpha. See [ROADMAP.md](ROADMAP.md).

## Design docs

- [RFC-0001: Wormhole Core](docs/rfcs/wormhole_rfc.md)
- [RFC-0002: Wormhole Governance](docs/rfcs/wormhole_rfc_governance.md)

## Stack

Go, PostgreSQL + pgvector, MCP.

## License

See [LICENSE](LICENSE).
