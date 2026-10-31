// PreToolUse: CLAUDE.md requires make; direct go build skips the dist/ layout.
const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const cmd = input.tool_input?.command ?? '';
if (/(^|[;&|(]\s*)go\s+build\b/m.test(cmd)) {
  console.error('Use make (e.g. make mkv-audit) instead of go build. See CLAUDE.md.');
  process.exit(2);
}
