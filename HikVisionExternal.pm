package ZoneMinder::Control::HikVisionExternal;

use strict;
use warnings;
use ZoneMinder::Logger qw(:all);
require ZoneMinder::Base;
require ZoneMinder::Control;

our @ISA     = qw(ZoneMinder::Control);
our $VERSION = '1.10';

# Full path to the binary
my $BINARY = '/opt/ptz_control/ptz_control';

# Quote a value for the shell used to start the detached service.
sub _shquote {
    my $s = shift;
    $s = '' unless defined $s;
    $s =~ s/'/'"'"'/g;
    return "'$s'";
}

# One socket per monitor, so several cameras can run side by side.
sub _sock_path {
    my $self = shift;
    my $host = $self->{host} || '';
    my $port = $self->{port} || 8000;
    my $id   = $self->{Monitor}{Id} || '';
    $host =~ s/[^A-Za-z0-9._-]/_/g;
    my $name = 'ptz_' . ( length $id ? "${id}_" : '' ) . "${host}_${port}.sock";
    return "/tmp/$name";
}

sub open {
    my $self = shift;
    $self->loadMonitor();

    my $addr = $self->{Monitor}{ControlAddress} || '';
    unless ( $addr =~ m{^([^:]+):([^@]+)\@([^:]+):(\d+)$} ) {
        Error("ControlAddress must be user:pass\@host:port (current='$addr')");
        return;
    }
    ($self->{user}, $self->{pass}, $self->{host}, $self->{port})
      = ($1, $2, $3, $4);
    $self->{port} ||= 8000;

    my $sock = $self->{sock_path} = _sock_path($self);

    # Start the detached service with nohup and capture its PID
    my $log = '/var/log/zm/ptz_service.log';
    my $cmd = join( ' ',
      'nohup',                          # ignore hangups
      _shquote($BINARY), 'serve',
      _shquote($self->{host}),
      _shquote($self->{port}),
      _shquote($self->{user}),
      _shquote($self->{pass}),
      _shquote($sock),
      '>>', _shquote($log), '2>&1',     # logs in ptz_service.log
      '&',                              # background
      'echo $!'                         # returns the PID of the background job
    );

    Info("HikVisionExternal: starting PTZ service: $cmd");
    my $pid = qx($cmd);
    chomp $pid;

    unless ( $pid =~ /^\d+$/ ) {
        Error("HikVisionExternal: unable to start ptz_control: $pid");
        return;
    }

    $self->{ptz_pid} = $pid;
    # give ptz_control a moment to create its socket
    sleep 1;

    $self->{state}    = 'open';
    $self->{last_cmd} = undef;
    Info("HikVisionExternal: PTZ service started (pid=$pid, socket=$sock)");
    return 1;
}

sub close {
    my $self = shift;
    if ( my $pid = delete $self->{ptz_pid} ) {
        Info("HikVisionExternal: stopping service (pid=$pid)");
        kill 'TERM', $pid;
        # the service removes its own socket on SIGTERM
    }
    $self->{state} = 'closed';
    Info("HikVisionExternal closed");
    return 1;
}

sub sendPTZCommand {
    my ( $self, $cmd, $act ) = @_;
    my $sock = $self->{sock_path}
      or return Error("HikVisionExternal: no socket path (open() not called?)");
    Info("HikVisionExternal: sending PTZ $cmd $act via $sock");
    my $rc = system( $BINARY, $cmd, $act, $sock );
    if ( $rc != 0 ) {
        Error("HikVisionExternal: '$BINARY $cmd $act $sock' failed rc=$rc");
        return 0;
    }
    return 1;
}

sub moveConLeft   { my $s=shift; $s->{last_cmd}='LEFT';  $s->sendPTZCommand('LEFT','START') }
sub moveConRight  { my $s=shift; $s->{last_cmd}='RIGHT'; $s->sendPTZCommand('RIGHT','START') }
sub moveConUp     { my $s=shift; $s->{last_cmd}='UP';    $s->sendPTZCommand('UP','START') }
sub moveConDown   { my $s=shift; $s->{last_cmd}='DOWN';  $s->sendPTZCommand('DOWN','START') }

sub moveStop {
    my $self = shift;
    return unless $self->{state} eq 'open';
    my $cmd = $self->{last_cmd}
      or return Error("moveStop without a previous command");
    $self->sendPTZCommand( $cmd, 'STOP' );
}

1;
