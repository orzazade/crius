const PROVIDERS = {
  codex: {
    command: "codex",
    role: "implementation"
  },
  claude: {
    command: "claude",
    role: "implementation"
  },
  gemini: {
    command: "gemini",
    role: "implementation"
  }
};

export function listLocalCliProviders() {
  return Object.keys(PROVIDERS);
}

export function getLocalCliProvider(name) {
  const provider = PROVIDERS[name];
  if (!provider) {
    throw new Error(
      `Unknown provider "${name}". Supported providers: ${listLocalCliProviders().join(", ")}`
    );
  }

  return {
    name,
    ...provider
  };
}

