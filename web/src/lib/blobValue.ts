// spec/encoding.md §5 的 $blob 值形状判定（纯逻辑、无 IO）。
// 网格标签、复制菜单与 §5.2 旁路取回共用同一套判定，避免各组件各自解读。

/** 内联形状 {"len":N,"base64":"..."}（§5.1）；旁路引用形状 {"len":N,"ref":"$polydb_blob:<uuid>"}（§5.2）。 */
export interface BlobValue {
  len: number;
  base64?: string;
  ref?: string;
}

/** §5.2 旁路引用前缀，与 Go protocol.BlobRefPrefix / Rust BLOB_REF_PREFIX 一致。 */
export const BLOB_REF_PREFIX = '$polydb_blob:';

/** 识别 $blob 值：必须有数值 len；base64/ref 按需存在，其余形状返回 null。 */
export function asBlobValue(v: unknown): BlobValue | null {
  if (typeof v !== 'object' || v === null) return null;
  const inner = (v as Record<string, unknown>)['$blob'];
  if (typeof inner !== 'object' || inner === null) return null;
  const len = (inner as Record<string, unknown>)['len'];
  if (typeof len !== 'number') return null;
  const base64 = (inner as Record<string, unknown>)['base64'];
  const ref = (inner as Record<string, unknown>)['ref'];
  return {
    len,
    base64: typeof base64 === 'string' ? base64 : undefined,
    ref: typeof ref === 'string' ? ref : undefined,
  };
}

/** ref → REST 取回路径使用的 UUID 部分（$polydb_blob:<uuid>）；前缀非法返回 null。 */
export function blobIdOfRef(ref: string): string | null {
  return ref.startsWith(BLOB_REF_PREFIX) ? ref.slice(BLOB_REF_PREFIX.length) : null;
}

/** 网格/Inspector/复制统一的 blob 占位标签（spec/encoding.md §5.1）。 */
export function blobLabel(len: number): string {
  return `<blob ${len} bytes>`;
}
