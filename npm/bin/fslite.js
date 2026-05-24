#!/usr/bin/env node
//
// fslite — node shim that execs the Go binary installed by
// ../install.js at ../bin/fslite. Forwards argv + signals + exit
// code transparently.

'use strict';

const { spawnSync } = require('child_process');
const path = require('path');
const os = require('os');
const fs = require('fs');

const binaryName = os.platform() === 'win32' ? 'fslite.exe' : 'fslite';
const binaryPath = path.join(__dirname, binaryName);

if (!fs.existsSync(binaryPath)) {
  console.error(
    'fslite: binary not found at ' + binaryPath + '\n' +
      '  Run `npm rebuild fslite` to (re)build the Go binary.'
  );
  process.exit(127);
}

const result = spawnSync(binaryPath, process.argv.slice(2), {
  stdio: 'inherit',
});
if (result.error) {
  console.error('fslite: ' + result.error.message);
  process.exit(1);
}
process.exit(result.status || 0);
