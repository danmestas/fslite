#!/usr/bin/env node
//
// Smoke test for the npm package: run `fslite --help` via the shim
// and verify it produces output mentioning the `mcp` subcommand.

'use strict';

const { spawnSync } = require('child_process');
const path = require('path');

const shim = path.join(__dirname, 'bin', 'fslite.js');
const result = spawnSync(process.execPath, [shim, '--help'], {
  encoding: 'utf8',
});

if (result.status !== 0) {
  console.error('fslite --help exited ' + result.status);
  console.error(result.stdout);
  console.error(result.stderr);
  process.exit(1);
}

const out = result.stdout + result.stderr;
const needles = ['mcp', 'serve', 'demo', 'commit'];
for (const needle of needles) {
  if (!out.includes(needle)) {
    console.error('fslite --help missing expected token: ' + needle);
    process.exit(1);
  }
}
console.log('fslite npm smoke test passed.');
