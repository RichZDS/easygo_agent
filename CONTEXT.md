# Agent Conversation

This context describes the durable conversation concepts visible to users and operators.

## Language

**Turn**:
A complete interaction for one user intent, beginning with a user input and ending in a terminal outcome. Tool work, human confirmation, retries, and resumptions remain part of the same Turn; a new ordinary user input starts a new Turn.
_Avoid_: Round, request, task

**Cancellation**:
A terminal end to a Turn. A cancelled Turn cannot be resumed.
_Avoid_: Interruption, pause

**Interruption**:
A non-terminal pause while a Turn waits for human input or approval. Resuming continues the same Turn.
_Avoid_: Cancellation, failure

**Message**:
A durable contribution to a Turn from a system, user, assistant, or tool. Its persisted fields mirror Eino `schema.Message` inside an EasyGo-owned envelope. Partial assistant output from a cancelled or failed Turn remains visible, but only completed Messages are eligible as context for a later Turn.
_Avoid_: Event, chunk

**Active Turn**:
The single non-terminal Turn currently allowed to extend a Session. Different Sessions may have Active Turns concurrently, but a Session cannot have more than one.
_Avoid_: Worker job, stream lease

**Agent Configuration**:
A versioned definition of an Agent's instructions and behavior. Its system instruction is applied when a Turn runs and is not duplicated into ordinary conversation history.
_Avoid_: System Message, model response

**Agent Event**:
An ephemeral Eino ADK event produced while a Runner executes. It drives the live SSE projection and terminal Message persistence, but is not itself durable conversation history.
_Avoid_: Message, replay record
