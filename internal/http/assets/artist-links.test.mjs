import assert from 'node:assert/strict';
import test from 'node:test';
import { fitArtistCount } from './artist-links.js';

const chip = hidden => 20 + String(hidden).length * 8;

test('shows every name when the line is wide enough', () => {
  assert.equal(fitArtistCount([40, 40, 40], 12, 6, 200, chip), 3);
});

test('fits as many complete names as the chip leaves room for', () => {
  // 50*4 + 10*3 = 230 > 180; two names: 100 + 10 + 6 + chip(2) = 146
  assert.equal(fitArtistCount([50, 50, 50, 50], 10, 6, 180, () => 30), 2);
});

test('keeps one name when even two names plus the chip overflow', () => {
  assert.equal(fitArtistCount([80, 80, 80, 80, 80], 12, 6, 180, chip), 1);
});

test('returns zero when there is nothing to show', () => {
  assert.equal(fitArtistCount([], 12, 6, 180, chip), 0);
  assert.equal(fitArtistCount([40], 12, 6, 0, chip), 0);
});
