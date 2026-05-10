// Monkey-patch Bun.serve to inject a generous idleTimeout.
// copilot-api uses Bun.serve internally with the default 10s idle timeout,
// which is too short for LLM streaming responses (30-120s+).
const originalServe = Bun.serve.bind(Bun);
Bun.serve = function (options) {
  return originalServe({ ...options, idleTimeout: 255 }); // max 255s
};
