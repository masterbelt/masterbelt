import type { UserConfig } from "@commitlint/types";
import { RuleConfigSeverity } from "@commitlint/types";

const Configuration: UserConfig = {
  extends: ["@commitlint/config-conventional", "agent-coauthor"],
  formatter: "@commitlint/format",
  // Dependabot capitalizes its subject ("build: Bump …"), which trips
  // subject-case, and there is no Dependabot setting to reword it. Skip linting
  // the commits it signs off on rather than loosen subject-case for every author.
  ignores: [(message) => /^Signed-off-by: dependabot\[bot\]/m.test(message)],
  rules: {
    "type-enum": [
      RuleConfigSeverity.Error,
      "always",
      [
        "build",
        "chore",
        "ci",
        "docs",
        "feat",
        "fix",
        "merge",
        "perf",
        "refactor",
        "revert",
        "style",
        "test",
      ],
    ],
  },
};

export default Configuration;
