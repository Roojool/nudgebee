import { KB_FORMAT_PDF, KB_FORMAT_TEXT, MAX_UPLOAD_BYTES, arrayBufferToBase64, isPdfFile, isTextFile } from '../kbUploadFormat';

const fileOf = (name: string, type: string): File => ({ name, type } as File);

describe('kbUploadFormat', () => {
  describe('isPdfFile', () => {
    it('recognises a PDF by MIME type', () => {
      expect(isPdfFile(fileOf('runbook.pdf', 'application/pdf'))).toBe(true);
    });

    it('recognises a PDF by extension when the browser reports no MIME type', () => {
      // Drag-and-drop from some file managers gives an empty type; without the
      // extension fallback the file would be rejected as unsupported.
      expect(isPdfFile(fileOf('runbook.PDF', ''))).toBe(true);
    });

    it('does not treat a text file as a PDF', () => {
      expect(isPdfFile(fileOf('notes.txt', 'text/plain'))).toBe(false);
    });

    it('does not match a name that merely contains pdf', () => {
      expect(isPdfFile(fileOf('pdf-notes.txt', 'text/plain'))).toBe(false);
    });
  });

  describe('isTextFile', () => {
    it('recognises a text file by type or extension', () => {
      expect(isTextFile(fileOf('notes.txt', ''))).toBe(true);
      expect(isTextFile(fileOf('notes', 'text/plain'))).toBe(true);
      expect(isTextFile(fileOf('runbook.pdf', 'application/pdf'))).toBe(false);
    });
  });

  describe('arrayBufferToBase64', () => {
    it('round-trips bytes through base64', () => {
      const bytes = new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d]); // "%PDF-"
      expect(arrayBufferToBase64(bytes.buffer)).toBe('JVBERi0=');
    });

    it('encodes an empty buffer to an empty string', () => {
      expect(arrayBufferToBase64(new Uint8Array([]).buffer)).toBe('');
    });

    it('encodes a buffer larger than one chunk', () => {
      // 0x8000 is the chunk boundary; a naive single fromCharCode spread over a
      // buffer this size throws, which is the bug the chunking exists to avoid.
      const size = 0x8000 * 2 + 17;
      const bytes = new Uint8Array(size);
      for (let i = 0; i < size; i += 1) {
        bytes[i] = i % 256;
      }

      const encoded = arrayBufferToBase64(bytes.buffer);

      expect(encoded).toHaveLength(Math.ceil(size / 3) * 4);
      const decoded = Uint8Array.from(atob(encoded), (c) => c.charCodeAt(0));
      expect(decoded).toHaveLength(size);
      expect(decoded[0]).toBe(0);
      expect(decoded[size - 1]).toBe((size - 1) % 256);
    });

    it('preserves high bytes that are not valid UTF-8', () => {
      const bytes = new Uint8Array([0xff, 0xfe, 0x00, 0x80]);
      const decoded = Uint8Array.from(atob(arrayBufferToBase64(bytes.buffer)), (c) => c.charCodeAt(0));
      expect(Array.from(decoded)).toEqual([0xff, 0xfe, 0x00, 0x80]);
    });
  });

  it('exposes the format identifiers the backend expects', () => {
    expect(KB_FORMAT_TEXT).toBe('text');
    expect(KB_FORMAT_PDF).toBe('pdf');
    expect(MAX_UPLOAD_BYTES).toBe(5 * 1024 * 1024);
  });
});
