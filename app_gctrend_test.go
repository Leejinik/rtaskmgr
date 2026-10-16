package main

import "testing"

func TestModuleName(t *testing.T) {
	cases := map[string][]string{
		"collector": {"java", "-Dmodule.name=collector", "-Xmx1g", "-jar", "/usr/local/liz/lizcollector/liz-collector.jar"},
		"liz-admin": {"java", "-Xloggc:/x/gc.log", "-jar", "/usr/local/liz/lizadmin/liz-admin.jar", "--spring.config.location=classpath:/a"},
		"cas":       {"java", "-jar", "/usr/local/liz/sso/cas.war"},
		"tomcat":    {"/usr/bin/java", "-classpath", "/a/bootstrap.jar:/a/tomcat-juli.jar", "org.apache.catalina.startup.Bootstrap", "start"},
		"kafka":     {"java", "-Xmx1G", "-cp", "/usr/local/liz/kafka/libs/a.jar", "kafka.Kafka", "/cfg/server.properties"},
		"zookeeper": {"java", "-cp", "x", "org.apache.zookeeper.server.quorum.QuorumPeerMain", "zoo.cfg"},
		"Main":      {"java", "com.example.Main"},
	}
	for want, args := range cases {
		if got := moduleName(args); got != want {
			t.Errorf("%v → %q, want %q", args, got, want)
		}
	}
}
