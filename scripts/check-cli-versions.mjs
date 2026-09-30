#!/usr/bin/env node
// The task runtime image and the workshop host image each pin the same four
// coding CLIs with `ARG <NAME>_VERSION=`. Fail when a pin is missing, repeated
// with different values, or differs between the two files.
// Usage: node scripts/check-cli-versions.mjs [DOCKERFILE_A DOCKERFILE_B]
import { readFileSync } from 'node:fs';
import { relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(fileURLToPath(new URL('..', import.meta.url)));
const names = ['CODEX_VERSION', 'CLAUDE_CODE_VERSION', 'PI_VERSION', 'OPENCLAW_VERSION'];
const args = process.argv.slice(2);
if (args.length !== 0 && args.length !== 2) {
  console.error('Usage: node scripts/check-cli-versions.mjs [DOCKERFILE_A DOCKERFILE_B]');
  process.exit(2);
}
const files = (args.length ? args : ['deploy/runtime/Dockerfile', 'services/workshop/Dockerfile.host']).map((file) =>
  resolve(root, file)
);
const label = (file) => (relative(root, file).startsWith('..') ? file : relative(root, file));

function pins(file) {
  const found = new Map(names.map((name) => [name, new Set()]));
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    const match = /^\s*ARG\s+([A-Z_]+)=["']?([^"'\s]+)["']?\s*$/.exec(line);
    if (match && found.has(match[1])) found.get(match[1]).add(match[2]);
  }
  return found;
}

const pinned = files.map(pins);
const problems = [];
for (const name of names) {
  const values = pinned.map((found) => [...found.get(name)]);
  values.forEach((list, i) => {
    if (list.length === 0) problems.push(`${name}: missing in ${label(files[i])}`);
    if (list.length > 1) problems.push(`${name}: ${label(files[i])} pins several values (${list.join(', ')})`);
  });
  const [left, right] = values;
  if (left.length === 1 && right.length === 1 && left[0] !== right[0]) {
    problems.push(`${name}: ${label(files[0])}=${left[0]} but ${label(files[1])}=${right[0]}`);
  }
}

if (problems.length) {
  console.error(`CLI version pins disagree:\n${problems.map((problem) => `  ${problem}`).join('\n')}`);
  process.exit(1);
}
console.log(
  `CLI version pins match in ${files.map(label).join(' and ')}: ${names.map((name) => `${name}=${[...pinned[0].get(name)][0]}`).join(' ')}`
);
