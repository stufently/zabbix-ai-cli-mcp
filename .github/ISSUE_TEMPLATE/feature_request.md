---
name: Feature request
about: A task the tool does not cover yet
labels: enhancement
---

**The question you were trying to answer**

Describe the operational task, not the API method. This project deliberately
exposes task-shaped commands rather than a mapping of the Zabbix API, so
"find out why a host went quiet" is more useful here than "expose item.get".

**How you do it today**

Which commands, or which raw `api call`, you currently chain together.

**Why the escape hatch is not enough**

`zabbix-ai-cli-mcp api call` already reaches any classified method. A dedicated
command earns its place when the raw calls have to be chained, when the result
needs bounding, or when the API's own behaviour is misleading.
