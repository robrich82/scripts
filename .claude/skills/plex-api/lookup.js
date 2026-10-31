const fs = require('fs');
const path = require('path');

const spec = JSON.parse(fs.readFileSync(path.join(__dirname, '../../../openapi.json'), 'utf8'));
const METHODS = ['get', 'post', 'put', 'patch', 'delete', 'head', 'options'];

function search(pattern) {
  const re = new RegExp(pattern, 'i');
  for (const [route, item] of Object.entries(spec.paths)) {
    for (const method of METHODS.filter(m => item[m])) {
      const op = item[method];
      const text = [route, op.summary, op.operationId, ...(op.tags ?? [])].join(' ');
      if (re.test(text)) console.log(`${method.toUpperCase()} ${route} — ${op.summary ?? ''}`);
    }
  }
}

function collectRefs(node, seen) {
  if (Array.isArray(node)) return node.forEach(n => collectRefs(n, seen));
  if (!node || typeof node !== 'object') return;
  for (const [key, value] of Object.entries(node)) {
    if (key === '$ref' && typeof value === 'string' && !seen.has(value)) {
      seen.add(value);
      collectRefs(value.slice(2).split('/').reduce((n, k) => n?.[k], spec), seen);
    } else {
      collectRefs(value, seen);
    }
  }
}

function detail(arg) {
  const route = arg.startsWith('/') ? arg : `/${arg}`;
  const item = spec.paths[route];
  if (!item) {
    console.error(`No such path: ${route}`);
    process.exit(1);
  }
  const seen = new Set();
  collectRefs(item, seen);
  const refs = Object.fromEntries(
    [...seen].map(ref => [ref, ref.slice(2).split('/').reduce((n, k) => n?.[k], spec)]),
  );
  console.log(JSON.stringify({ [route]: item, refs }, null, 2));
}

const [first, second] = process.argv.slice(2);
if (first === '--detail' && second) detail(second);
else if (first && first !== '--detail') search(first);
else {
  console.error('Usage: lookup.js <regex> | lookup.js --detail <path>');
  process.exit(1);
}
