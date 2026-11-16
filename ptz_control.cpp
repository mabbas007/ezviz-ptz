// ptz_control.cpp
// Single binary, two modes:
//   ./ptz_control serve <ip> <port> <user> <pass> [socket]
//       -> starts the PTZ service (SDK login + UNIX socket)
//   ./ptz_control <CMD> <START|STOP> [speed] [socket]
//       -> sends the command to the service
//   ./ptz_control POS [socket]      -> reports the raw PTZ position
//   ./ptz_control INFO [socket]     -> reports the encoder configuration
//   ./ptz_control PING [socket]     -> liveness check
//
// The socket path defaults to /tmp/ptzd.socket, can be overridden by the
// optional argument or the PTZ_SOCK environment variable. Each service
// instance should use its own socket so several cameras can run side by side.

#include "HCNetSDK.h"
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <signal.h>

#define DEFAULT_SOCK_PATH "/tmp/ptzd.socket"
#define BUFSIZE           128
#define MAX_SPEED         7

static char g_sock_path[sizeof(((struct sockaddr_un*)0)->sun_path)];

static int getCmd(const char* s) {
  if (!strcmp(s,"LEFT"))     return PAN_LEFT;
  if (!strcmp(s,"RIGHT"))    return PAN_RIGHT;
  if (!strcmp(s,"UP"))       return TILT_UP;
  if (!strcmp(s,"DOWN"))     return TILT_DOWN;
  if (!strcmp(s,"ZOOM_IN"))  return ZOOM_IN;
  if (!strcmp(s,"ZOOM_OUT")) return ZOOM_OUT;
  return -1;
}

// Resolve the socket path: explicit argument, then $PTZ_SOCK, then default.
static void set_sock_path(const char* p) {
  if (!p || !*p) p = getenv("PTZ_SOCK");
  if (!p || !*p) p = DEFAULT_SOCK_PATH;
  if (strlen(p) >= sizeof(g_sock_path)) {
    fprintf(stderr,"[ptz] socket path too long (max %zu): %s\n",
            sizeof(g_sock_path)-1, p);
    exit(1);
  }
  strcpy(g_sock_path, p);
}

// Map NET_DVR_COMPRESSION_INFO_V30.byResolution to pixel dimensions. The
// field is a BYTE index into the vendor table documented in
// HCNetSDKCom/incEn/HCNetSDK.h (NET_DVR_COMPRESSION_INFO_V30, lines
// 9922-10020). That table continues past 255, but a BYTE can only carry
// 0-254 (255 = auto), so those entries are unreachable here. Indices the
// table leaves undefined (8-11, 14-15, 25-26, 61, 158-159, 246-253) return
// 0x0 so the Go bridge can fall back to VIDEO_WIDTH/VIDEO_HEIGHT.
//
// Entries 0-5, 20, 21, 27, 37 and 49 are named only in the header
// (DCIF/CIF/QCIF/4CIF/2CIF/2QCIF, XVGA, HD900p, HD1080i/1080i, WSXGA+);
// their dimensions are the standard values for those names.
static void resolution_dims(BYTE index, int* width, int* height) {
  switch (index) {
    case 0: *width = 528; *height = 384; break;
    case 1: *width = 352; *height = 288; break;
    case 2: *width = 176; *height = 144; break;
    case 3: *width = 704; *height = 576; break;
    case 4: *width = 704; *height = 288; break;
    case 5: *width = 352; *height = 144; break;
    case 6: *width = 320; *height = 240; break;
    case 7: *width = 160; *height = 120; break;
    case 12: *width = 384; *height = 288; break;
    case 13: *width = 576; *height = 576; break;
    case 16: *width = 640; *height = 480; break;
    case 17: *width = 1600; *height = 1200; break;
    case 18: *width = 800; *height = 600; break;
    case 19: *width = 1280; *height = 720; break;
    case 20: *width = 1280; *height = 960; break;
    case 21: *width = 1600; *height = 900; break;
    case 22: *width = 1360; *height = 1024; break;
    case 23: *width = 1536; *height = 1536; break;
    case 24: *width = 1920; *height = 1920; break;
    case 27: *width = 1920; *height = 1080; break;
    case 28: *width = 2560; *height = 1920; break;
    case 29: *width = 1600; *height = 304; break;
    case 30: *width = 2048; *height = 1536; break;
    case 31: *width = 2448; *height = 2048; break;
    case 32: *width = 2448; *height = 1200; break;
    case 33: *width = 2448; *height = 800; break;
    case 34: *width = 1024; *height = 768; break;
    case 35: *width = 1280; *height = 1024; break;
    case 36: *width = 960; *height = 576; break;
    case 37: *width = 1920; *height = 1080; break;
    case 38: *width = 1440; *height = 900; break;
    case 39: *width = 1920; *height = 1080; break;
    case 40: *width = 1920; *height = 540; break;
    case 41: *width = 960; *height = 540; break;
    case 42: *width = 2336; *height = 1744; break;
    case 43: *width = 1920; *height = 1456; break;
    case 44: *width = 2592; *height = 2048; break;
    case 45: *width = 3296; *height = 2472; break;
    case 46: *width = 1376; *height = 768; break;
    case 47: *width = 1366; *height = 768; break;
    case 48: *width = 1360; *height = 768; break;
    case 49: *width = 1680; *height = 1050; break;
    case 50: *width = 720; *height = 720; break;
    case 51: *width = 1280; *height = 1280; break;
    case 52: *width = 2048; *height = 768; break;
    case 53: *width = 2048; *height = 2048; break;
    case 54: *width = 2560; *height = 2048; break;
    case 55: *width = 3072; *height = 2048; break;
    case 56: *width = 2304; *height = 1296; break;
    case 57: *width = 1280; *height = 800; break;
    case 58: *width = 1600; *height = 600; break;
    case 59: *width = 1600; *height = 900; break;
    case 60: *width = 2752; *height = 2208; break;
    case 62: *width = 4000; *height = 3000; break;
    case 63: *width = 4096; *height = 2160; break;
    case 64: *width = 3840; *height = 2160; break;
    case 65: *width = 4000; *height = 2250; break;
    case 66: *width = 3072; *height = 1728; break;
    case 67: *width = 2592; *height = 1944; break;
    case 68: *width = 2464; *height = 1520; break;
    case 69: *width = 1280; *height = 1920; break;
    case 70: *width = 2560; *height = 1440; break;
    case 71: *width = 1024; *height = 1024; break;
    case 72: *width = 160; *height = 128; break;
    case 73: *width = 324; *height = 240; break;
    case 74: *width = 324; *height = 256; break;
    case 75: *width = 336; *height = 256; break;
    case 76: *width = 640; *height = 512; break;
    case 77: *width = 2720; *height = 2048; break;
    case 78: *width = 384; *height = 256; break;
    case 79: *width = 384; *height = 216; break;
    case 80: *width = 320; *height = 256; break;
    case 81: *width = 320; *height = 180; break;
    case 82: *width = 320; *height = 192; break;
    case 83: *width = 512; *height = 384; break;
    case 84: *width = 325; *height = 256; break;
    case 85: *width = 256; *height = 192; break;
    case 86: *width = 640; *height = 360; break;
    case 87: *width = 1776; *height = 1340; break;
    case 88: *width = 1936; *height = 1092; break;
    case 89: *width = 2080; *height = 784; break;
    case 90: *width = 2144; *height = 604; break;
    case 91: *width = 1920; *height = 1200; break;
    case 92: *width = 4064; *height = 3040; break;
    case 93: *width = 3040; *height = 3040; break;
    case 94: *width = 3072; *height = 2304; break;
    case 95: *width = 3072; *height = 1152; break;
    case 96: *width = 2560; *height = 2560; break;
    case 97: *width = 2688; *height = 1536; break;
    case 98: *width = 2688; *height = 1520; break;
    case 99: *width = 3072; *height = 3072; break;
    case 100: *width = 3392; *height = 2008; break;
    case 101: *width = 4000; *height = 3080; break;
    case 102: *width = 960; *height = 720; break;
    case 103: *width = 1024; *height = 1536; break;
    case 104: *width = 704; *height = 1056; break;
    case 105: *width = 352; *height = 528; break;
    case 106: *width = 2048; *height = 1530; break;
    case 107: *width = 2560; *height = 1600; break;
    case 108: *width = 2800; *height = 2100; break;
    case 109: *width = 4088; *height = 4088; break;
    case 110: *width = 4000; *height = 3072; break;
    case 111: *width = 960; *height = 1080; break;
    case 112: *width = 640; *height = 720; break;
    case 113: *width = 640; *height = 960; break;
    case 114: *width = 320; *height = 480; break;
    case 115: *width = 3840; *height = 2400; break;
    case 116: *width = 3840; *height = 1680; break;
    case 117: *width = 2560; *height = 1120; break;
    case 118: *width = 704; *height = 320; break;
    case 119: *width = 1200; *height = 1920; break;
    case 120: *width = 480; *height = 768; break;
    case 121: *width = 768; *height = 480; break;
    case 122: *width = 320; *height = 512; break;
    case 123: *width = 512; *height = 320; break;
    case 124: *width = 4096; *height = 1800; break;
    case 125: *width = 1280; *height = 560; break;
    case 126: *width = 2400; *height = 3840; break;
    case 127: *width = 480; *height = 272; break;
    case 128: *width = 512; *height = 272; break;
    case 129: *width = 2592; *height = 2592; break;
    case 130: *width = 1792; *height = 2880; break;
    case 131: *width = 1600; *height = 2560; break;
    case 132: *width = 2720; *height = 1192; break;
    case 133: *width = 1920; *height = 1536; break;
    case 134: *width = 2560; *height = 1944; break;
    case 135: *width = 2560; *height = 960; break;
    case 136: *width = 2064; *height = 1544; break;
    case 137: *width = 4096; *height = 1200; break;
    case 138: *width = 3840; *height = 1080; break;
    case 139: *width = 2720; *height = 800; break;
    case 140: *width = 512; *height = 232; break;
    case 141: *width = 704; *height = 200; break;
    case 142: *width = 512; *height = 152; break;
    case 143: *width = 2048; *height = 896; break;
    case 144: *width = 2048; *height = 600; break;
    case 145: *width = 1280; *height = 376; break;
    case 146: *width = 2592; *height = 1520; break;
    case 147: *width = 2592; *height = 1536; break;
    case 148: *width = 3072; *height = 8192; break;
    case 149: *width = 768; *height = 2048; break;
    case 150: *width = 8208; *height = 3072; break;
    case 151: *width = 4096; *height = 1536; break;
    case 152: *width = 6912; *height = 2800; break;
    case 153: *width = 3456; *height = 1400; break;
    case 154: *width = 480; *height = 720; break;
    case 155: *width = 800; *height = 450; break;
    case 156: *width = 480; *height = 270; break;
    case 157: *width = 2560; *height = 1536; break;
    case 160: *width = 3264; *height = 2448; break;
    case 161: *width = 288; *height = 320; break;
    case 162: *width = 144; *height = 176; break;
    case 163: *width = 480; *height = 640; break;
    case 164: *width = 240; *height = 320; break;
    case 165: *width = 120; *height = 160; break;
    case 166: *width = 576; *height = 720; break;
    case 167: *width = 720; *height = 1280; break;
    case 168: *width = 576; *height = 960; break;
    case 169: *width = 2944; *height = 1656; break;
    case 170: *width = 432; *height = 240; break;
    case 171: *width = 2160; *height = 3840; break;
    case 172: *width = 1080; *height = 1920; break;
    case 173: *width = 7008; *height = 1080; break;
    case 174: *width = 3504; *height = 540; break;
    case 175: *width = 1752; *height = 270; break;
    case 176: *width = 876; *height = 135; break;
    case 177: *width = 4096; *height = 1440; break;
    case 178: *width = 4096; *height = 1080; break;
    case 179: *width = 1536; *height = 864; break;
    case 180: *width = 180; *height = 240; break;
    case 181: *width = 360; *height = 480; break;
    case 182: *width = 540; *height = 720; break;
    case 183: *width = 720; *height = 960; break;
    case 184: *width = 960; *height = 1280; break;
    case 185: *width = 1080; *height = 1440; break;
    case 186: *width = 3200; *height = 1800; break;
    case 187: *width = 1752; *height = 272; break;
    case 188: *width = 872; *height = 136; break;
    case 189: *width = 1280; *height = 1440; break;
    case 190: *width = 3520; *height = 544; break;
    case 191: *width = 1728; *height = 256; break;
    case 192: *width = 864; *height = 128; break;
    case 193: *width = 5470; *height = 3684; break;
    case 194: *width = 2560; *height = 750; break;
    case 195: *width = 1600; *height = 480; break;
    case 196: *width = 1024; *height = 300; break;
    case 197: *width = 848; *height = 480; break;
    case 198: *width = 8160; *height = 3616; break;
    case 199: *width = 8160; *height = 2304; break;
    case 200: *width = 4064; *height = 2560; break;
    case 201: *width = 1400; *height = 1050; break;
    case 202: *width = 2688; *height = 1512; break;
    case 203: *width = 704; *height = 288; break;
    case 204: *width = 1560; *height = 656; break;
    case 205: *width = 3072; *height = 1280; break;
    case 206: *width = 4320; *height = 1808; break;
    case 207: *width = 6120; *height = 2560; break;
    case 208: *width = 1280; *height = 960; break;
    case 209: *width = 3000; *height = 3000; break;
    case 210: *width = 2032; *height = 3616; break;
    case 211: *width = 7680; *height = 4320; break;
    case 212: *width = 5760; *height = 3240; break;
    case 213: *width = 3264; *height = 1836; break;
    case 214: *width = 2712; *height = 1536; break;
    case 215: *width = 1080; *height = 720; break;
    case 216: *width = 360; *height = 640; break;
    case 217: *width = 1440; *height = 812; break;
    case 218: *width = 8160; *height = 2416; break;
    case 219: *width = 5760; *height = 1712; break;
    case 220: *width = 4080; *height = 1200; break;
    case 221: *width = 6144; *height = 2560; break;
    case 222: *width = 1568; *height = 656; break;
    case 223: *width = 4096; *height = 1808; break;
    case 224: *width = 2720; *height = 1200; break;
    case 225: *width = 2896; *height = 848; break;
    case 226: *width = 2048; *height = 608; break;
    case 227: *width = 3840; *height = 1120; break;
    case 228: *width = 2720; *height = 784; break;
    case 229: *width = 1920; *height = 560; break;
    case 230: *width = 1360; *height = 400; break;
    case 231: *width = 704; *height = 208; break;
    case 232: *width = 496; *height = 144; break;
    case 233: *width = 352; *height = 128; break;
    case 234: *width = 512; *height = 240; break;
    case 235: *width = 512; *height = 160; break;
    case 236: *width = 368; *height = 128; break;
    case 237: *width = 256; *height = 128; break;
    case 238: *width = 2048; *height = 592; break;
    case 239: *width = 1456; *height = 416; break;
    case 240: *width = 1024; *height = 304; break;
    case 241: *width = 1280; *height = 368; break;
    case 242: *width = 912; *height = 256; break;
    case 243: *width = 640; *height = 192; break;
    case 244: *width = 252; *height = 112; break;
    case 245: *width = 576; *height = 704; break;
    // 254 = read NET_DVR_MULTI_STREAM_COMPRESSIONCFG->dwResolution;
    // 255 = auto/current. Both fall back to the configured VIDEO_* values.
    default: *width = 0; *height = 0; break;
  }
}

// ONVIF tt:VideoEncoding values only: 0/1 H264, 2 MPEG4, 7 JPEG, 10 H265.
static const char* encoding_name(BYTE type) {
  switch (type) {
    case 0:
    case 1:  return "H264";
    case 2:  return "MPEG4";
    case 7:  return "JPEG";
    case 10: return "H265";
    default: return "UNKNOWN";
  }
}

// dwVideoFrameRate is an index, not frames per second.
static int frame_rate(DWORD index) {
  static const int table[] = {
    0, 0, 0, 0, 1, 1, 2, 4, 6, 8, 10, 12, 16, 20, 15, 18, 22, 25, 30, 35,
    40, 45, 50, 55, 60, 3, 5, 7, 9, 100, 120, 24, 48, 8
  };
  if (index < sizeof(table) / sizeof(table[0])) return table[index];
  return 0;
}

// dwVideoBitrate is either an index or (when bit 31 is set) explicit kbps.
static int bitrate_kbps(DWORD value, WORD average) {
  static const int table[] = {
    0, 16, 32, 48, 64, 80, 96, 128, 160, 192, 224, 256, 320, 384,
    448, 512, 640, 768, 896, 1024, 1280, 1536, 1792, 2048, 3072,
    4096, 8192, 16384
  };
  if (value & 0x80000000u) return (int)(value & 0x7fffffffu);
  if (value < sizeof(table) / sizeof(table[0]) && value > 0) return table[value];
  return average;
}

// INFO: report the network encoder configuration to the bridge. The bridge
// uses it for the ONVIF media profile; it does not change the stream.
static void send_info(int cl, LONG uid, DWORD chan) {
  NET_DVR_COMPRESSIONCFG_V30 cfg;
  DWORD ret = 0;
  memset(&cfg, 0, sizeof(cfg));
  if (!NET_DVR_GetDVRConfig(uid, NET_DVR_GET_COMPRESSCFG_V30, chan,
                            &cfg, sizeof(cfg), &ret)) {
    dprintf(cl, "ERR %u\n", NET_DVR_GetLastError());
    return;
  }

  const NET_DVR_COMPRESSION_INFO_V30* p = &cfg.struNetPara;
  int width = 0, height = 0;
  resolution_dims(p->byResolution, &width, &height);
  dprintf(cl, "INFO enc=%s width=%d height=%d fps=%d bitrate=%d\n",
          encoding_name(p->byVideoEncType), width, height,
          frame_rate(p->dwVideoFrameRate),
          bitrate_kbps(p->dwVideoBitrate, p->wAverageVideoBitrate));
}

// Remove the socket when the service is stopped.
static void on_term(int) {
  if (g_sock_path[0]) unlink(g_sock_path);
  _exit(0);
}

// Handle one command read from a client. Returns the reply to send.
static void handle_command(int cl, LONG uid, DWORD chan, char* buf) {
  char a[16] = {0}, b[16] = {0}, c[16] = {0};
  int nf = sscanf(buf, "%15s %15s %15s", a, b, c);

  if (nf < 1) {
    dprintf(cl, "ERR_CMD\n");
    return;
  }

  if (!strcmp(a, "PING")) {
    dprintf(cl, "PONG\n");
    return;
  }

  if (!strcmp(a, "INFO")) {
    send_info(cl, uid, chan);
    return;
  }

  if (!strcmp(a, "POS")) {
    NET_DVR_PTZPOS pos;
    DWORD ret = 0;
    memset(&pos, 0, sizeof(pos));
    if (NET_DVR_GetDVRConfig(uid, NET_DVR_GET_PTZPOS, chan,
                             &pos, sizeof(pos), &ret))
      dprintf(cl, "POS %u %u %u\n", pos.wPanPos, pos.wTiltPos, pos.wZoomPos);
    else
      dprintf(cl, "ERR %u\n", NET_DVR_GetLastError());
    return;
  }

  // Movement command: <CMD> <START|STOP> [speed]
  int cmd = getCmd(a);
  if (cmd < 0 || nf < 2) {
    dprintf(cl, "ERR_CMD\n");
    return;
  }
  DWORD stop  = (strcmp(b, "STOP") == 0);
  DWORD speed = 0;
  if (nf >= 3) {
    speed = (DWORD)strtoul(c, NULL, 10);
    if (speed < 1) speed = 1;
    if (speed > MAX_SPEED) speed = MAX_SPEED;
  }

  BOOL ok;
  if (!stop && speed)
    ok = NET_DVR_PTZControlWithSpeed_Other(uid, chan, cmd, 0, speed);
  else
    ok = NET_DVR_PTZControl_Other(uid, chan, cmd, stop);

  if (!ok)
    dprintf(cl, "ERR %u\n", NET_DVR_GetLastError());
  else
    dprintf(cl, "OK\n");
}

static void run_service(char* ip, int port, char* user, char* pass) {
  // 1) init + login
  if (!NET_DVR_Init()) {
    fprintf(stderr,"[ptz] NET_DVR_Init failed %u\n",NET_DVR_GetLastError());
    exit(1);
  }
  NET_DVR_DEVICEINFO_V30 di;
  memset(&di, 0, sizeof(di));
  LONG uid = NET_DVR_Login_V30(ip, (WORD)port, user, pass, &di);
  if (uid < 0) {
    fprintf(stderr,"[ptz] Login failed %u\n",NET_DVR_GetLastError());
    NET_DVR_Cleanup();
    exit(1);
  }
  DWORD chan = di.byStartChan;

  // 2) UNIX socket setup
  unlink(g_sock_path);
  int ls = socket(AF_UNIX, SOCK_STREAM, 0);
  if (ls < 0) {
    perror("[ptz] socket");
    exit(1);
  }
  struct sockaddr_un sa;
  memset(&sa, 0, sizeof(sa));
  sa.sun_family = AF_UNIX;
  strcpy(sa.sun_path, g_sock_path);
  if (bind(ls,(struct sockaddr*)&sa,sizeof(sa)) < 0) {
    perror("[ptz] bind");
    exit(1);
  }
  if (listen(ls,5) < 0) {
    perror("[ptz] listen");
    exit(1);
  }
  chmod(g_sock_path,0666);
  fprintf(stderr,"[ptz] service up on %s (channel %u)\n",g_sock_path,chan);

  // 3) service loop
  for (;;) {
    int cl = accept(ls, NULL, NULL);
    if (cl < 0) continue;
    char buf[BUFSIZE];
    ssize_t n = read(cl,buf,sizeof(buf)-1);
    if (n > 0) {
      buf[n] = 0;
      handle_command(cl, uid, chan, buf);
    }
    close(cl);
  }
}

static void usage(char* prog) {
  printf("Usage:\n"
         "  %s serve <ip> <port> <user> <pass> [socket]\n"
         "     -> starts the PTZ service (SDK login, UNIX socket)\n"
         "  %s <CMD> <START|STOP> [speed] [socket]\n"
         "     -> sends a movement command (speed 1-7)\n"
         "  %s POS [socket]\n"
         "  %s INFO [socket]\n"
         "  %s PING [socket]\n"
         "CMD = LEFT | RIGHT | UP | DOWN | ZOOM_IN | ZOOM_OUT\n"
         "Socket defaults to $PTZ_SOCK, then %s\n",
         prog, prog, prog, prog, prog, DEFAULT_SOCK_PATH);
  exit(1);
}

int main(int argc, char** argv) {
  signal(SIGPIPE, SIG_IGN);

  if (argc >= 6 && !strcmp(argv[1],"serve")) {
    // service mode: serve <ip> <port> <user> <pass> [socket]
    set_sock_path(argc >= 7 ? argv[6] : NULL);
    signal(SIGTERM, on_term);
    signal(SIGINT,  on_term);
    run_service(argv[2], atoi(argv[3]), argv[4], argv[5]);
    return 0;
  }

  // client mode: the socket is the last argument, when present.
  if (argc < 2) usage(argv[0]);
  const char* cmd = argv[1];
  const char* act = NULL;
  const char* arg = NULL;
  const char* sock_arg = NULL;

  if (!strcmp(cmd, "POS") || !strcmp(cmd, "INFO") || !strcmp(cmd, "PING")) {
    if (argc >= 3) sock_arg = argv[2];
  } else {
    if (argc < 3) usage(argv[0]);
    act = argv[2];
    // optional speed, then optional socket
    if (argc >= 4) {
      if (strspn(argv[3], "0123456789") == strlen(argv[3]))
        arg = argv[3];
      else
        sock_arg = argv[3];
    }
    if (argc >= 5) sock_arg = argv[4];
  }

  set_sock_path(sock_arg);

  struct sockaddr_un sa;
  memset(&sa, 0, sizeof(sa));
  sa.sun_family = AF_UNIX;
  strcpy(sa.sun_path, g_sock_path);

  int s = socket(AF_UNIX, SOCK_STREAM, 0);
  if (s < 0) {
    perror("[ptz] socket");
    return 1;
  }
  if (connect(s,(struct sockaddr*)&sa,sizeof(sa)) < 0) {
    fprintf(stderr,"[ptz] connect %s: %s\n", g_sock_path, strerror(errno));
    close(s);
    return 1;
  }

  char msg[BUFSIZE];
  if (arg)
    snprintf(msg,sizeof(msg),"%s %s %s\n",cmd,act,arg);
  else if (act)
    snprintf(msg,sizeof(msg),"%s %s\n",cmd,act);
  else
    snprintf(msg,sizeof(msg),"%s\n",cmd);

  if (write(s,msg,strlen(msg)) < 0) {
    perror("[ptz] write");
    close(s);
    return 1;
  }

  char resp[64];
  ssize_t r = read(s,resp,sizeof(resp)-1);
  close(s);
  if (r <= 0) {
    fprintf(stderr,"[ptz] no response from service\n");
    return 1;
  }
  resp[r] = 0;
  printf("%s",resp);
  // PONG and POS are successful replies too, not just OK.
  if (strncmp(resp,"OK",2) == 0) return 0;
  if (strncmp(resp,"PONG",4) == 0) return 0;
  if (strncmp(resp,"POS ",4) == 0) return 0;
  if (strncmp(resp,"INFO ",5) == 0) return 0;
  return 1;
}
