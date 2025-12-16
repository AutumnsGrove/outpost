/**
 * RCON Client for Minecraft Server
 * Implements the Source RCON protocol used by Minecraft
 */

const RCON_PACKET_TYPE = {
  AUTH: 3,
  AUTH_RESPONSE: 2,
  COMMAND: 2,
  COMMAND_RESPONSE: 0,
};

interface RconPacket {
  id: number;
  type: number;
  body: string;
}

/**
 * Encode an RCON packet
 */
function encodePacket(packet: RconPacket): ArrayBuffer {
  const bodyBytes = new TextEncoder().encode(packet.body);
  // Packet: length (4) + id (4) + type (4) + body + null (1) + null (1)
  const length = 4 + 4 + bodyBytes.length + 1 + 1;
  const buffer = new ArrayBuffer(4 + length);
  const view = new DataView(buffer);

  view.setInt32(0, length, true); // Packet length (little-endian)
  view.setInt32(4, packet.id, true); // Request ID
  view.setInt32(8, packet.type, true); // Packet type

  // Body
  const bodyArray = new Uint8Array(buffer, 12);
  bodyArray.set(bodyBytes);
  // Null terminators already 0 from ArrayBuffer initialization

  return buffer;
}

/**
 * Decode an RCON packet from buffer
 */
function decodePacket(buffer: ArrayBuffer): RconPacket | null {
  if (buffer.byteLength < 14) return null;

  const view = new DataView(buffer);
  const length = view.getInt32(0, true);

  if (buffer.byteLength < 4 + length) return null;

  const id = view.getInt32(4, true);
  const type = view.getInt32(8, true);

  // Body is from byte 12 to end minus 2 null terminators
  const bodyBytes = new Uint8Array(buffer, 12, length - 10);
  const body = new TextDecoder().decode(bodyBytes);

  return { id, type, body };
}

/**
 * Send RCON command to Minecraft server
 */
export async function sendRconCommand(
  host: string,
  port: number,
  password: string,
  command: string
): Promise<{ success: boolean; response?: string; error?: string }> {
  try {
    // Connect to RCON server
    const socket = await connectTcp(host, port);

    // Authenticate
    const authPacket = encodePacket({
      id: 1,
      type: RCON_PACKET_TYPE.AUTH,
      body: password,
    });

    await socket.write(authPacket);
    const authResponse = await socket.read();

    if (!authResponse) {
      socket.close();
      return { success: false, error: 'No auth response from server' };
    }

    const authResult = decodePacket(authResponse);
    if (!authResult || authResult.id === -1) {
      socket.close();
      return { success: false, error: 'RCON authentication failed' };
    }

    // Send command
    const cmdPacket = encodePacket({
      id: 2,
      type: RCON_PACKET_TYPE.COMMAND,
      body: command,
    });

    await socket.write(cmdPacket);
    const cmdResponse = await socket.read();

    socket.close();

    if (!cmdResponse) {
      return { success: true, response: '' };
    }

    const cmdResult = decodePacket(cmdResponse);
    return {
      success: true,
      response: cmdResult?.body || '',
    };
  } catch (error) {
    const errorMsg = error instanceof Error ? error.message : String(error);
    return { success: false, error: `RCON connection failed: ${errorMsg}` };
  }
}

/**
 * TCP socket wrapper for Cloudflare Workers
 * Uses the connect() API available in Workers
 */
interface TcpSocket {
  write: (data: ArrayBuffer) => Promise<void>;
  read: () => Promise<ArrayBuffer | null>;
  close: () => void;
}

async function connectTcp(host: string, port: number): Promise<TcpSocket> {
  // @ts-ignore - Cloudflare Workers TCP API
  const socket = await connect({ hostname: host, port });

  const writer = socket.writable.getWriter();
  const reader = socket.readable.getReader();

  return {
    write: async (data: ArrayBuffer) => {
      await writer.write(new Uint8Array(data));
    },
    read: async () => {
      // Read with timeout
      const timeoutPromise = new Promise<null>((resolve) =>
        setTimeout(() => resolve(null), 5000)
      );

      const readPromise = (async () => {
        try {
          const { value, done } = await reader.read();
          if (done || !value) return null;
          return value.buffer as ArrayBuffer;
        } catch {
          return null;
        }
      })();

      return Promise.race([readPromise, timeoutPromise]);
    },
    close: () => {
      try {
        writer.close();
        reader.cancel();
      } catch {
        // Ignore close errors
      }
    },
  };
}
