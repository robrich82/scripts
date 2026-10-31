// PostToolUse: Write leaves CRLF on Windows, which make vet's gofmt -l rejects.
const { execFileSync } = require('child_process');
const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const file = input.tool_input?.file_path;
if (!file?.endsWith('.go')) process.exit(0);
try {
  execFileSync('gofmt', ['-w', file], { stdio: ['ignore', 'ignore', 'pipe'] });
} catch (e) {
  process.stderr.write(e.stderr?.toString() ?? String(e));
  process.exit(2);
}
