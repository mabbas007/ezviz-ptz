// ptz_control.cpp
// Single binary, two modes:
//   ./ptz_control serve <ip> <port> <user> <pass> [socket]
//       -> starts the PTZ service (SDK login + UNIX socket)
//   ./ptz_control <CMD> <START|STOP> [speed] [socket]
//       -> sends the command to the service
//   ./ptz_control POS [socket]      -> reports the raw PTZ position
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
         "  %s PING [socket]\n"
         "CMD = LEFT | RIGHT | UP | DOWN | ZOOM_IN | ZOOM_OUT\n"
         "Socket defaults to $PTZ_SOCK, then %s\n",
         prog, prog, prog, prog, DEFAULT_SOCK_PATH);
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

  if (!strcmp(cmd, "POS") || !strcmp(cmd, "PING")) {
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
  return 1;
}
