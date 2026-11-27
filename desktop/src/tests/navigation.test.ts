import assert from 'node:assert/strict';
import { test } from 'node:test';
import { isAdminURL, isExternalURL } from '../main/navigation.js';

test('admin navigation requires the exact local origin', () => {
  const origin = 'http://127.0.0.1:19866';
  for (const url of [
    origin,
    `${origin}/#bootstrap=code`,
    `${origin}/api/status`,
  ])
    assert.ok(isAdminURL(url, origin));
  for (const url of [
    'http://127.0.0.1:19865',
    'http://localhost:19866',
    'http://127.0.0.1:19866.example.com',
    'http://user@127.0.0.1:19866',
    'https://127.0.0.1:19866',
    'file:///etc/hosts',
    'javascript:alert(1)',
    'invalid',
  ])
    assert.equal(isAdminURL(url, origin), false);
});

test('external navigation accepts HTTP and HTTPS links', () => {
  assert.ok(isExternalURL('https://example.com/'));
  assert.ok(isExternalURL('http://example.com/'));
  for (const url of [
    'file:///etc/hosts',
    'javascript:alert(1)',
    'data:text/html,hello',
    'invalid',
  ])
    assert.equal(isExternalURL(url), false);
});
