import { describe, expect, it } from 'vitest';
import { asBlobValue, blobIdOfRef, blobLabel, BLOB_REF_PREFIX } from './blobValue';

describe('asBlobValue', () => {
  it('识别内联形状（§5.1）', () => {
    expect(asBlobValue({ $blob: { len: 3, base64: 'AAEC' } })).toEqual({ len: 3, base64: 'AAEC', ref: undefined });
  });

  it('识别旁路引用形状（§5.2）', () => {
    const ref = `${BLOB_REF_PREFIX}00000000-0000-0000-0000-000000000000`;
    expect(asBlobValue({ $blob: { len: 2097153, ref } })).toEqual({ len: 2097153, base64: undefined, ref });
  });

  it('非 $blob、缺 len、类型不符一律 null', () => {
    expect(asBlobValue(null)).toBeNull();
    expect(asBlobValue('x')).toBeNull();
    expect(asBlobValue({ a: 1 })).toBeNull();
    expect(asBlobValue({ $blob: { base64: 'AAEC' } })).toBeNull();
    expect(asBlobValue({ $blob: { len: '3' } })).toBeNull();
    expect(asBlobValue({ $blob: null })).toBeNull();
  });
});

describe('blobIdOfRef', () => {
  it('剥掉 $polydb_blob: 前缀取 UUID 部分', () => {
    expect(blobIdOfRef(`${BLOB_REF_PREFIX}6ba7b810-9dad-11d1-80b4-00c04fd430c8`))
      .toBe('6ba7b810-9dad-11d1-80b4-00c04fd430c8');
  });

  it('前缀非法返回 null', () => {
    expect(blobIdOfRef('6ba7b810-9dad-11d1-80b4-00c04fd430c8')).toBeNull();
    expect(blobIdOfRef('$other:x')).toBeNull();
  });
});

describe('blobLabel', () => {
  it('与网格/菜单占位一致', () => {
    expect(blobLabel(0)).toBe('<blob 0 bytes>');
    expect(blobLabel(1048577)).toBe('<blob 1048577 bytes>');
  });
});
